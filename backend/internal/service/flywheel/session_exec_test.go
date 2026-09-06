package flywheel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// fakeRunner simulates AO's session service: it makes a worktree, provisions the
// requested files, and (as if the agent ran) writes the decision file.
type fakeRunner struct {
	decision triageRouting
	files    map[string]string
	killed   []string
}

func (f *fakeRunner) SpawnWorker(_ context.Context, in WorkerSpawn) (WorkerHandle, error) {
	dir, err := os.MkdirTemp("", "fw-sess-test-*")
	if err != nil {
		return WorkerHandle{}, err
	}
	f.files = in.Files
	for name, content := range in.Files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
	}
	_ = os.WriteFile(filepath.Join(dir, flywheelDecisionFile), []byte(mustJSON(f.decision)), 0o600)
	return WorkerHandle{SessionID: "sess-fake", WorktreePath: dir}, nil
}

func (f *fakeRunner) KillWorker(_ context.Context, id string) error {
	f.killed = append(f.killed, id)
	return nil
}

func TestWorkerSessionExecutorCollectsDecisionAndProvisionsTools(t *testing.T) {
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
	// The mock MCP tools were provisioned into the worktree.
	if _, ok := f.files[".mcp.json"]; !ok {
		t.Fatalf("expected .mcp.json provisioned, got %v", f.files)
	}
	// The session was tidied up.
	if len(f.killed) != 1 || f.killed[0] != "sess-fake" {
		t.Fatalf("expected the session killed, got %v", f.killed)
	}
}

func TestWorkerSessionExecutorInjectsMemory(t *testing.T) {
	f := &fakeRunner{decision: triageRouting{Team: "Billing", Priority: 1}}
	exec := NewWorkerSessionExecutor(f)
	exec.PollInterval = 10 * time.Millisecond

	mem := []domain.FlywheelMemoryEntry{{
		Kind: domain.MemoryKnowledge, ScopeTask: triageTaskType,
		Title: "Route enterprise dup_charge -> Billing/P1", Confidence: 0.9,
	}}
	_, err := exec.Run(context.Background(), Task{
		ProjectID: "fw", TaskType: triageTaskType,
		InputJSON: mustJSON(triageInput{Tier: "enterprise", Keyword: "dup_charge"}),
	}, mem)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := f.files["FLYWHEEL.md"]; !ok {
		t.Fatalf("expected learned memory provisioned as FLYWHEEL.md, got %v", keysOf(f.files))
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

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
