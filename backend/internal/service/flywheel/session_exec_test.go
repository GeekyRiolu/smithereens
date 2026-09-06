package flywheel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// fakeRunner simulates AO's session service: it makes a worktree, records the
// spawn prompt, and (as if the agent ran) writes the decision file.
type fakeRunner struct {
	decision triageRouting
	prompt   string
	killed   []string
}

func (f *fakeRunner) SpawnWorker(_ context.Context, in WorkerSpawn) (WorkerHandle, error) {
	dir, err := os.MkdirTemp("", "fw-sess-test-*")
	if err != nil {
		return WorkerHandle{}, err
	}
	f.prompt = in.Prompt
	_ = os.WriteFile(filepath.Join(dir, flywheelDecisionFile), []byte(mustJSON(f.decision)), 0o600)
	return WorkerHandle{SessionID: "sess-fake", WorktreePath: dir}, nil
}

func (f *fakeRunner) KillWorker(_ context.Context, id string) error {
	f.killed = append(f.killed, id)
	return nil
}

func TestWorkerSessionExecutorCollectsDecisionAndTidies(t *testing.T) {
	f := &fakeRunner{decision: triageRouting{Team: "Billing", Priority: 1}}
	exec := NewWorkerSessionExecutor(f)
	exec.PollInterval = 10 * time.Millisecond
	exec.Timeout = 2 * time.Second

	traj, err := exec.Run(context.Background(), Task{
		ProjectID: "fw", TaskType: triageTaskType,
		InputJSON: mustJSON(triageInput{Tier: "enterprise", Keyword: "dup_charge"}),
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var got triageRouting
	if err := json.Unmarshal([]byte(traj.OutcomeJSON), &got); err != nil {
		t.Fatalf("decode outcome: %v", err)
	}
	if got != (triageRouting{Team: "Billing", Priority: 1}) {
		t.Fatalf("expected collected routing Billing/1, got %+v", got)
	}
	// The prompt instructs writing the decision file and carries the tier.
	if !strings.Contains(f.prompt, flywheelDecisionFile) || !strings.Contains(f.prompt, "enterprise") {
		t.Fatalf("prompt missing decision-file directive or tier:\n%s", f.prompt)
	}
	// The session was tidied up.
	if len(f.killed) != 1 || f.killed[0] != "sess-fake" {
		t.Fatalf("expected the session killed, got %v", f.killed)
	}
}

func TestWorkerSessionExecutorEmbedsMemoryInPrompt(t *testing.T) {
	f := &fakeRunner{decision: triageRouting{Team: "Billing", Priority: 1}}
	exec := NewWorkerSessionExecutor(f)
	exec.PollInterval = 10 * time.Millisecond

	mem := []domain.FlywheelMemoryEntry{{
		Kind: domain.MemoryKnowledge, ScopeTask: triageTaskType,
		Title: "Route enterprise dup_charge -> Billing/P1", Confidence: 0.9,
	}}
	if _, err := exec.Run(context.Background(), Task{
		ProjectID: "fw", TaskType: triageTaskType,
		InputJSON: mustJSON(triageInput{Tier: "enterprise", Keyword: "dup_charge"}),
	}, mem); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(f.prompt, "Route enterprise dup_charge -> Billing/P1") {
		t.Fatalf("expected learned memory embedded in the prompt:\n%s", f.prompt)
	}
}

// noDecisionRunner never writes the decision file, to exercise the timeout path.
type noDecisionRunner struct{ killed []string }

func (r *noDecisionRunner) SpawnWorker(_ context.Context, _ WorkerSpawn) (WorkerHandle, error) {
	dir, _ := os.MkdirTemp("", "fw-sess-noop-*")
	return WorkerHandle{SessionID: "sess-noop", WorktreePath: dir}, nil
}
func (r *noDecisionRunner) KillWorker(_ context.Context, id string) error {
	r.killed = append(r.killed, id)
	return nil
}

func TestWorkerSessionExecutorTimesOutAndTidies(t *testing.T) {
	r := &noDecisionRunner{}
	exec := NewWorkerSessionExecutor(r)
	exec.PollInterval = 5 * time.Millisecond
	exec.Timeout = 40 * time.Millisecond

	_, err := exec.Run(context.Background(), Task{
		ProjectID: "fw", TaskType: triageTaskType,
		InputJSON: mustJSON(triageInput{Tier: "free", Keyword: "howto"}),
	}, nil)
	if err == nil {
		t.Fatal("expected a timeout error when the worker writes no decision")
	}
	if len(r.killed) != 1 {
		t.Fatalf("expected the session tidied even on timeout, got %v", r.killed)
	}
}
