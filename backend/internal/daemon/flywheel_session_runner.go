package daemon

import (
	"context"
	"fmt"
	"os"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/flywheel"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

// flywheelSessionRunner adapts AO's session service to flywheel.SessionRunner so
// the Flywheel worker-session demo (Option 2) can spawn real worker agents that
// appear on the Kanban and feed the learning loop. See
// flywheel/ORCHESTRATION-BRIDGE.md.
type flywheelSessionRunner struct {
	sessions *sessionsvc.Service
}

func newFlywheelSessionRunner(sessions *sessionsvc.Service) *flywheelSessionRunner {
	return &flywheelSessionRunner{sessions: sessions}
}

// SpawnWorker spawns a claude-code worker for one triage task. Permissions are
// bypassed so the managed session can use its tools and write its decision file
// without an interactive approver.
func (r *flywheelSessionRunner) SpawnWorker(ctx context.Context, in flywheel.WorkerSpawn) (flywheel.WorkerHandle, error) {
	sess, _, _, err := r.sessions.Spawn(ctx, ports.SpawnConfig{
		ProjectID:   domain.ProjectID(in.ProjectID),
		Kind:        domain.KindWorker,
		Harness:     domain.HarnessClaudeCode,
		Prompt:      in.Prompt,
		DisplayName: in.AgentLabel,
		AgentConfig: ports.AgentConfig{
			Model:       os.Getenv("AO_FLYWHEEL_MODEL"), // empty = project/agent default
			Permissions: domain.PermissionModeBypassPermissions,
		},
	})
	if err != nil {
		return flywheel.WorkerHandle{}, fmt.Errorf("spawn flywheel worker: %w", err)
	}
	return flywheel.WorkerHandle{
		SessionID:    string(sess.ID),
		WorktreePath: sess.Metadata.WorkspacePath,
	}, nil
}

// KillWorker tears the worker session down (best-effort; dirty worktrees are
// preserved by the manager).
func (r *flywheelSessionRunner) KillWorker(ctx context.Context, sessionID string) error {
	if _, err := r.sessions.Kill(ctx, domain.SessionID(sessionID)); err != nil {
		return fmt.Errorf("kill flywheel worker: %w", err)
	}
	return nil
}
