package flywheel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Option 2: run tasks as REAL AO worker sessions (spawned like the orchestrator
// spawns workers) so the runs appear on the Kanban and feed the learning loop.
// See flywheel/ORCHESTRATION-BRIDGE.md.
//
// Managed sessions launch the agent inside Spawn (before we could write files
// into the worktree), and managed claude-code has no --mcp-config, so the task
// context + learned memory are embedded in the spawn PROMPT. The agent completes
// the task and writes its decision to flywheel-decision.json in its worktree
// (using its built-in Write tool); we poll for that file — a deterministic,
// transcript-free way to read the outcome.

// WorkerSpawn describes a worker session to launch for one Flywheel task.
type WorkerSpawn struct {
	ProjectID  string
	AgentLabel string // attribution / display name
	Prompt     string // the full task instruction (context + memory + write-file directive)
}

// WorkerHandle is a spawned worker session.
type WorkerHandle struct {
	SessionID    string
	WorktreePath string
}

// SessionRunner spawns and tidies real AO worker sessions. The concrete
// implementation (wired in the daemon over AO's session service) is separate; a
// fake backs the unit tests. Dependency inversion keeps service/flywheel free of
// a hard dependency on service/session.
type SessionRunner interface {
	SpawnWorker(ctx context.Context, in WorkerSpawn) (WorkerHandle, error)
	KillWorker(ctx context.Context, sessionID string) error
}

const flywheelDecisionFile = "flywheel-decision.json"

// WorkerSessionExecutor runs each task as a real AO worker session.
type WorkerSessionExecutor struct {
	Runner       SessionRunner
	Timeout      time.Duration
	PollInterval time.Duration
}

// NewWorkerSessionExecutor builds a session executor over the given runner.
func NewWorkerSessionExecutor(runner SessionRunner) *WorkerSessionExecutor {
	return &WorkerSessionExecutor{
		Runner:       runner,
		Timeout:      6 * time.Minute,
		PollInterval: 3 * time.Second,
	}
}

// Run implements Executor.
func (e *WorkerSessionExecutor) Run(ctx context.Context, task Task, memory []domain.FlywheelMemoryEntry) (Trajectory, error) {
	var in triageInput
	_ = json.Unmarshal([]byte(task.InputJSON), &in)

	handle, err := e.Runner.SpawnWorker(ctx, WorkerSpawn{
		ProjectID:  task.ProjectID,
		AgentLabel: "flywheel triage",
		Prompt:     e.sessionPrompt(in, ComposeLearnedContext(memory)),
	})
	if err != nil {
		return Trajectory{}, fmt.Errorf("session: spawn worker: %w", err)
	}
	defer func() {
		// Best-effort tidy, even if the run's context was cancelled.
		_ = e.Runner.KillWorker(context.WithoutCancel(ctx), handle.SessionID)
	}()

	routing, err := e.awaitDecision(ctx, handle.WorktreePath)
	if err != nil {
		return Trajectory{}, err
	}
	trace := []map[string]any{
		{"step": "worker_session", "sessionId": handle.SessionID},
		{"step": "route", "team": routing.Team, "priority": routing.Priority},
	}
	return Trajectory{
		OutcomeJSON: mustJSON(routing),
		TraceJSON:   mustJSON(trace),
		// Token/cost from the session's usage activities is a follow-up; the
		// learning signal here is routing correctness.
	}, nil
}

func (e *WorkerSessionExecutor) awaitDecision(ctx context.Context, worktree string) (triageRouting, error) {
	path := filepath.Join(worktree, flywheelDecisionFile)
	deadline := time.Now().Add(e.Timeout)
	poll := e.PollInterval
	if poll <= 0 {
		poll = 3 * time.Second
	}
	for {
		if data, err := os.ReadFile(path); err == nil {
			if r, ok := extractRouting(string(data)); ok {
				return r, nil
			}
		}
		if time.Now().After(deadline) {
			return triageRouting{}, fmt.Errorf("session: worker did not write %s within %s", flywheelDecisionFile, e.Timeout)
		}
		select {
		case <-ctx.Done():
			return triageRouting{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (e *WorkerSessionExecutor) sessionPrompt(in triageInput, memoryBlock string) string {
	prompt := fmt.Sprintf(
		"You are a support-triage worker. A customer (reference %s, plan tier: %s) wrote: %q "+
			"(issue keyword: %s). Decide the correct team (Billing, Infra, or Support) and priority "+
			"(1=highest, 2, or 3).",
		refForTier(in.Tier), in.Tier, messageForKeyword(in.Keyword), in.Keyword,
	)
	if memoryBlock != "" {
		prompt += "\n\nLearned guidance from past runs (apply it where it fits):\n" + memoryBlock
	}
	prompt += fmt.Sprintf(
		"\n\nWhen you have decided, WRITE your answer as a single JSON object to a file named %s in your "+
			"current working directory, exactly like {\"team\":\"Billing\",\"priority\":2}. That file is how your "+
			"result is collected — you are done once it is written.",
		flywheelDecisionFile,
	)
	return prompt
}
