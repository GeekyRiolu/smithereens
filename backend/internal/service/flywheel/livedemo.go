package flywheel

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Live demo: run a small batch of REAL Claude Code agent episodes (cold, then
// warm once memory is promoted), so the app shows a genuine agent doing the task
// and improving. Real agent runs are long, so this runs in the background and
// the UI polls Overview for progress + records.

type liveRunState struct {
	running   bool
	startedAt time.Time
	step      string
	errMsg    string
}

// LiveStatus is the async live-demo progress the dashboard surfaces.
type LiveStatus struct {
	Running   bool       `json:"running"`
	Step      string     `json:"step"`
	Error     string     `json:"error,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// LiveAvailable reports whether the real `claude` CLI is present, so the UI can
// enable/disable the live button.
func LiveAvailable() bool {
	_, ok := NewLiveExecutor()
	return ok
}

func (s *Service) liveStatus(projectID string) LiveStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.live[projectID]
	if st == nil {
		return LiveStatus{}
	}
	out := LiveStatus{Running: st.running, Step: st.step, Error: st.errMsg}
	if !st.startedAt.IsZero() {
		t := st.startedAt
		out.StartedAt = &t
	}
	return out
}

func (s *Service) setLiveStep(projectID, step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.live[projectID]; st != nil {
		st.step = step
	}
}

// StartLiveDemo begins the live demo for a project in the background (idempotent
// while running). It returns the current status; an unavailable claude CLI is
// reported as an error status without starting a run.
func (s *Service) StartLiveDemo(projectID string) LiveStatus {
	if !LiveAvailable() {
		return LiveStatus{Error: "the `claude` CLI was not found on this machine; install/authenticate Claude Code to run the live demo"}
	}
	s.mu.Lock()
	if st := s.live[projectID]; st != nil && st.running {
		defer s.mu.Unlock()
		return LiveStatus{Running: true, Step: st.step, StartedAt: ptrTime(st.startedAt)}
	}
	st := &liveRunState{running: true, startedAt: s.now(), step: "starting"}
	s.live[projectID] = st
	s.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		_, err := s.runLive(ctx, projectID, func(step string) { s.setLiveStep(projectID, step) })
		s.mu.Lock()
		st.running = false
		st.step = "done"
		if err != nil {
			st.errMsg = err.Error()
			st.step = "failed"
		}
		s.mu.Unlock()
	}()

	return LiveStatus{Running: true, Step: "starting", StartedAt: ptrTime(st.startedAt)}
}

func ptrTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// liveBatch is the small set of tasks the live demo runs (real agent calls cost
// money + time, so keep it tight while still covering the tier-dependent rule
// and its negative twin).
func liveBatch() []Task {
	return triageBatch([]triageCombo{
		{"enterprise", "dup_charge", 1},
		{"free", "dup_charge", 1},
		{"enterprise", "outage", 1},
	})
}

// RunLiveTriageDemo runs the live demo synchronously and returns the cycle
// reports (used by tests and by the async wrapper).
func (s *Service) RunLiveTriageDemo(ctx context.Context, projectID string) ([]CycleReport, error) {
	return s.runLive(ctx, projectID, func(string) {})
}

func (s *Service) runLive(ctx context.Context, projectID string, setStep func(string)) ([]CycleReport, error) {
	live, ok := NewLiveExecutor()
	if !ok {
		return nil, fmt.Errorf("live executor unavailable: the `claude` CLI was not found")
	}
	sim := SimTriageExecutor{}                  // cheap, deterministic evaluator for the gate
	grader := TriageGrader{}                    // grades outcomes vs reference (state check)
	reflector := TriageReflector{MinSupport: 1} // few live episodes → low support bar

	if err := s.SeedTriageEvalSuite(ctx, projectID); err != nil {
		return nil, err
	}
	reports := make([]CycleReport, 0, 3)

	// Cold baseline (deterministic eval, memory off) — the starting curve point.
	setStep("scoring cold baseline")
	cold, err := s.EvaluateSuite(ctx, projectID, nil, sim, grader)
	if err != nil {
		return nil, err
	}
	coldRun, err := s.store.CreateFlywheelEvalRun(ctx, domain.FlywheelEvalRun{
		ProjectID: projectID, Ablation: "memory_on", MetricsJSON: mustJSON(cold),
	})
	if err != nil {
		return nil, err
	}
	coldCycle, err := s.store.CreateFlywheelConsolidationCycle(ctx, domain.FlywheelConsolidationCycle{
		ProjectID: projectID, EvalRunID: coldRun.ID,
		NetDeltaJSON: mustJSON(map[string]float64{"dAccuracy": 0}),
	})
	if err != nil {
		return nil, err
	}
	reports = append(reports, CycleReport{Cycle: coldCycle, MemoryOn: cold, MemoryOff: cold})

	tasks := liveBatch()

	// Cold LIVE batch — the real agent, no learned memory yet.
	setStep(fmt.Sprintf("running %d cold episodes with the real Claude Code agent", len(tasks)))
	if _, err := s.RunBatch(ctx, projectID, "claude-code · cold", tasks, live, grader, triageFeedback); err != nil {
		return nil, err
	}

	// Consolidate: reflect the real episodes, eval-gate with the fast evaluator.
	setStep("consolidating + eval-gating learned lessons")
	rep, err := s.Consolidate(ctx, projectID, sim, grader, reflector)
	if err != nil {
		return nil, err
	}
	reports = append(reports, rep)

	// Warm LIVE batch — the same tasks, now with promoted memory injected.
	setStep(fmt.Sprintf("running %d warm episodes (memory injected)", len(tasks)))
	if _, err := s.RunBatch(ctx, projectID, "claude-code · warm", tasks, live, grader, triageFeedback); err != nil {
		return nil, err
	}
	setStep("recording warm cycle")
	rep2, err := s.Consolidate(ctx, projectID, sim, grader, reflector)
	if err != nil {
		return nil, err
	}
	reports = append(reports, rep2)

	return reports, nil
}
