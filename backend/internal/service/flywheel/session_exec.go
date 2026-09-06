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

// WorkerSpawn describes a worker session to launch for one Flywheel task.
type WorkerSpawn struct {
	ProjectID  string
	AgentLabel string            // attribution recorded on the episode
	Prompt     string            // initial task instruction for the agent
	Files      map[string]string // worktree-relative files placed BEFORE the agent runs
}

// WorkerHandle is a spawned worker session.
type WorkerHandle struct {
	SessionID    string
	WorktreePath string
}

// SessionRunner spawns and tidies real AO worker sessions. The concrete
// implementation (wired in the daemon) wraps AO's session service; a fake backs
// the unit tests. Dependency inversion keeps service/flywheel free of a hard
// dependency on service/session.
type SessionRunner interface {
	SpawnWorker(ctx context.Context, in WorkerSpawn) (WorkerHandle, error)
	KillWorker(ctx context.Context, sessionID string) error
}

const flywheelDecisionFile = "flywheel-decision.json"

// WorkerSessionExecutor runs each task as a real AO worker session. It
// provisions the mock MCP tools + learned memory into the worktree, then
// collects the agent's decision from flywheel-decision.json — a deterministic,
// transcript-free way to read the outcome.
type WorkerSessionExecutor struct {
	Runner       SessionRunner
	AOBin        string // this daemon's binary; launches `ao flywheel-tools`
	Timeout      time.Duration
	PollInterval time.Duration
}

// NewWorkerSessionExecutor builds a session executor over the given runner.
func NewWorkerSessionExecutor(runner SessionRunner) *WorkerSessionExecutor {
	aoBin, _ := os.Executable()
	return &WorkerSessionExecutor{
		Runner:       runner,
		AOBin:        aoBin,
		Timeout:      5 * time.Minute,
		PollInterval: 2 * time.Second,
	}
}

// Run implements Executor.
func (e *WorkerSessionExecutor) Run(ctx context.Context, task Task, memory []domain.FlywheelMemoryEntry) (Trajectory, error) {
	var in triageInput
	_ = json.Unmarshal([]byte(task.InputJSON), &in)
	ref := refForTier(in.Tier)
	msg := messageForKeyword(in.Keyword)

	files := map[string]string{
		".mcp.json": fmt.Sprintf(`{"mcpServers":{"triage":{"command":%q,"args":["flywheel-tools"]}}}`, e.AOBin),
	}
	if block := ComposeLearnedContext(memory); block != "" {
		files["FLYWHEEL.md"] = block
	}

	handle, err := e.Runner.SpawnWorker(ctx, WorkerSpawn{
		ProjectID:  task.ProjectID,
		AgentLabel: "claude-code · worker",
		Prompt:     e.sessionPrompt(ref, msg, in.Keyword),
		Files:      files,
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
		// Token/cost from session usage is a follow-up; the learning signal here
		// is routing correctness.
	}, nil
}

func (e *WorkerSessionExecutor) awaitDecision(ctx context.Context, worktree string) (triageRouting, error) {
	path := filepath.Join(worktree, flywheelDecisionFile)
	deadline := time.Now().Add(e.Timeout)
	poll := e.PollInterval
	if poll <= 0 {
		poll = 2 * time.Second
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

func (e *WorkerSessionExecutor) sessionPrompt(ref, msg, keyword string) string {
	return fmt.Sprintf(
		"You are a support-triage worker. A customer (reference: %s) wrote: %q (issue keyword: %s). "+
			"Use the `triage` MCP tools to look up the customer's plan tier and analyze similar resolved "+
			"tickets, and apply any guidance in FLYWHEEL.md if that file is present. Decide the correct team "+
			"(Billing, Infra, or Support) and priority (1=highest, 2, or 3). "+
			"Then WRITE your decision as a single JSON object to a file named %s in the current directory, "+
			"exactly like {\"team\":\"Billing\",\"priority\":2}. That file is how your result is collected — "+
			"you are done once it is written.",
		ref, msg, keyword, flywheelDecisionFile,
	)
}
