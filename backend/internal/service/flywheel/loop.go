package flywheel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The learning loop. The fast loop (RunBatch) executes tasks and records
// episodes; the slow loop (Consolidate) reflects episodes into candidate
// lessons and promotes only those that pass the eval gate. Executor, Grader and
// Reflector are interfaces: the demo wires a deterministic simulated triage
// implementation (triage_demo.go), and the same seams accept an AO-session
// executor + LLM reflector in the app.

// Task is one unit of work handed to the executor.
type Task struct {
	ID        string
	TaskType  string
	InputJSON string
}

// Trajectory is what an executor produced for a task: the outcome plus the cost
// signals the eval curve tracks. OutcomeJSON is opaque to the loop; the Grader
// interprets it.
type Trajectory struct {
	OutcomeJSON string
	TraceJSON   string
	Tokens      int
	ToolCalls   int
	ToolErrors  int
	LatencyMS   int
	CostUSD     float64
}

// Executor runs a task using the supplied memory (the learned context). Only the
// memory changes across runs, which is what keeps "it got better" honest.
type Executor interface {
	Run(ctx context.Context, task Task, memory []domain.FlywheelMemoryEntry) (Trajectory, error)
}

// Grader scores a trajectory against a case's reference outcome (state/outcome
// verification). It never sees the memory the executor used.
type Grader interface {
	Grade(referenceJSON string, traj Trajectory) (bool, error)
}

// Reflector distills a batch of episodes into candidate lessons (with
// provenance). Deterministic rule-mining in the demo; an LLM reflector later.
type Reflector interface {
	Reflect(episodes []domain.FlywheelEpisode) ([]domain.FlywheelMemoryEntry, error)
}

// EvalMetrics is one scoring of the suite. It is a vector, not a scalar, so the
// gate can require quality to rise without cost rising.
type EvalMetrics struct {
	N             int     `json:"n"`
	Correct       int     `json:"correct"`
	Accuracy      float64 `json:"accuracy"`
	AvgTokens     float64 `json:"avgTokens"`
	AvgToolCalls  float64 `json:"avgToolCalls"`
	ToolErrorRate float64 `json:"toolErrorRate"`
}

const evalEps = 1e-9

// better reports whether m improves on base: strictly higher accuracy, or equal
// accuracy at strictly lower token cost. This is what promotes a lesson.
func (m EvalMetrics) better(base EvalMetrics) bool {
	if m.Accuracy > base.Accuracy+evalEps {
		return true
	}
	if m.Accuracy >= base.Accuracy-evalEps && m.AvgTokens < base.AvgTokens-evalEps {
		return true
	}
	return false
}

// EvaluateSuite runs the whole eval suite with a given memory set and returns
// aggregate metrics. Passing nil memory is the memory-off ablation arm.
func (s *Service) EvaluateSuite(ctx context.Context, projectID string, memory []domain.FlywheelMemoryEntry, exec Executor, grader Grader) (EvalMetrics, error) {
	cases, err := s.store.ListFlywheelEvalCases(ctx, projectID)
	if err != nil {
		return EvalMetrics{}, fmt.Errorf("load eval cases: %w", err)
	}
	var m EvalMetrics
	for _, c := range cases {
		traj, err := exec.Run(ctx, Task{ID: c.ID, TaskType: c.TaskType, InputJSON: c.InputJSON}, memory)
		if err != nil {
			return EvalMetrics{}, fmt.Errorf("eval case %s: %w", c.ID, err)
		}
		ok, err := grader.Grade(c.ReferenceJSON, traj)
		if err != nil {
			return EvalMetrics{}, fmt.Errorf("grade case %s: %w", c.ID, err)
		}
		m.N++
		if ok {
			m.Correct++
		}
		m.AvgTokens += float64(traj.Tokens)
		m.AvgToolCalls += float64(traj.ToolCalls)
		m.ToolErrorRate += float64(traj.ToolErrors)
	}
	if m.N > 0 {
		m.Accuracy = float64(m.Correct) / float64(m.N)
		m.AvgTokens /= float64(m.N)
		m.AvgToolCalls /= float64(m.N)
		m.ToolErrorRate /= float64(m.N)
	}
	return m, nil
}

// RunBatch executes tasks with the currently active memory and records one
// episode per task. feedback returns the observed correct outcome (resolved-
// ticket ground truth in the demo; real outcomes/corrections in the app) that
// the reflector later learns from. Returns the recorded episodes.
func (s *Service) RunBatch(ctx context.Context, projectID, agent string, tasks []Task, exec Executor, grader Grader, feedback func(Task) string) ([]domain.FlywheelEpisode, error) {
	out := make([]domain.FlywheelEpisode, 0, len(tasks))
	for _, task := range tasks {
		memory, err := s.Retrieve(ctx, projectID, task.TaskType, 0)
		if err != nil {
			return nil, fmt.Errorf("retrieve for %s: %w", task.ID, err)
		}
		traj, err := exec.Run(ctx, task, memory)
		if err != nil {
			return nil, fmt.Errorf("run %s: %w", task.ID, err)
		}
		ref := feedback(task)
		success, err := grader.Grade(ref, traj)
		if err != nil {
			return nil, fmt.Errorf("grade %s: %w", task.ID, err)
		}
		retrievedIDs := make([]string, len(memory))
		for i, mm := range memory {
			retrievedIDs[i] = mm.ID
		}
		ep, err := s.RecordEpisode(ctx, domain.FlywheelEpisode{
			ProjectID:     projectID,
			SessionID:     agent, // in the app this is the AO session id; here, the agent label
			TaskType:      task.TaskType,
			InputJSON:     withAgent(task.InputJSON, agent),
			RetrievedJSON: mustJSON(retrievedIDs),
			TraceJSON:     traj.TraceJSON,
			OutcomeJSON:   outcomeJSON(traj, success, agent),
			FeedbackJSON:  ref,
		})
		if err != nil {
			return nil, fmt.Errorf("record episode %s: %w", task.ID, err)
		}
		out = append(out, ep)
	}
	return out, nil
}

// Consolidate is the slow loop: reflect unreflected episodes into candidates,
// then eval-gate each — promote only if it improves the suite without
// regression, else quarantine. Writes the memory-on + memory-off (ablation)
// eval runs and the consolidation-cycle audit (the dual-curve point).
func (s *Service) Consolidate(ctx context.Context, projectID string, exec Executor, grader Grader, reflector Reflector) (CycleReport, error) {
	now := s.now()
	episodes, err := s.store.ListUnreflectedFlywheelEpisodes(ctx, projectID, 10000)
	if err != nil {
		return CycleReport{}, fmt.Errorf("load episodes: %w", err)
	}
	candidates, err := reflector.Reflect(episodes)
	if err != nil {
		return CycleReport{}, fmt.Errorf("reflect: %w", err)
	}
	ids, err := s.Propose(ctx, projectID, candidates)
	if err != nil {
		return CycleReport{}, fmt.Errorf("propose: %w", err)
	}

	active, err := s.store.ListFlywheelMemoryByStatus(ctx, projectID, domain.MemoryActive)
	if err != nil {
		return CycleReport{}, fmt.Errorf("load active: %w", err)
	}
	baseline, err := s.EvaluateSuite(ctx, projectID, active, exec, grader)
	if err != nil {
		return CycleReport{}, err
	}

	promoted, quarantined := 0, 0
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		entry, ok, err := s.store.GetFlywheelMemory(ctx, id)
		if err != nil {
			return CycleReport{}, err
		}
		if !ok || entry.Status != domain.MemoryCandidate {
			continue // merged corroboration of an existing lesson; nothing to gate
		}
		staged := append(append([]domain.FlywheelMemoryEntry{}, active...), entry)
		m, err := s.EvaluateSuite(ctx, projectID, staged, exec, grader)
		if err != nil {
			return CycleReport{}, err
		}
		if m.better(baseline) {
			if err := s.Promote(ctx, id); err != nil {
				return CycleReport{}, err
			}
			active = staged
			baseline = m
			promoted++
		} else {
			if err := s.Quarantine(ctx, id, "did not improve the eval suite without regression"); err != nil {
				return CycleReport{}, err
			}
			quarantined++
		}
	}

	memoryOn, err := s.EvaluateSuite(ctx, projectID, active, exec, grader)
	if err != nil {
		return CycleReport{}, err
	}
	memoryOff, err := s.EvaluateSuite(ctx, projectID, nil, exec, grader)
	if err != nil {
		return CycleReport{}, err
	}

	onRun, err := s.store.CreateFlywheelEvalRun(ctx, domain.FlywheelEvalRun{
		ProjectID: projectID, Ablation: "memory_on", MetricsJSON: mustJSON(memoryOn),
	})
	if err != nil {
		return CycleReport{}, err
	}
	if _, err := s.store.CreateFlywheelEvalRun(ctx, domain.FlywheelEvalRun{
		ProjectID: projectID, Ablation: "memory_off", MetricsJSON: mustJSON(memoryOff),
	}); err != nil {
		return CycleReport{}, err
	}

	netDelta := map[string]float64{
		"dAccuracy":  memoryOn.Accuracy - memoryOff.Accuracy,
		"dTokens":    memoryOn.AvgTokens - memoryOff.AvgTokens,
		"dToolCalls": memoryOn.AvgToolCalls - memoryOff.AvgToolCalls,
	}
	cycle, err := s.store.CreateFlywheelConsolidationCycle(ctx, domain.FlywheelConsolidationCycle{
		ProjectID: projectID, EpisodesSeen: len(episodes), Candidates: len(candidates),
		Promoted: promoted, Quarantined: quarantined, EvalRunID: onRun.ID,
		NetDeltaJSON: mustJSON(netDelta),
	})
	if err != nil {
		return CycleReport{}, err
	}

	for _, ep := range episodes {
		if err := s.store.MarkFlywheelEpisodeReflected(ctx, ep.ID, now); err != nil {
			return CycleReport{}, err
		}
	}

	return CycleReport{Cycle: cycle, MemoryOn: memoryOn, MemoryOff: memoryOff}, nil
}

// CycleReport bundles a consolidation cycle with its memory-on / memory-off
// (ablation) metrics — one point on the dual curve.
type CycleReport struct {
	Cycle     domain.FlywheelConsolidationCycle
	MemoryOn  EvalMetrics
	MemoryOff EvalMetrics
}

// ---- small JSON helpers -----------------------------------------------------

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func withAgent(inputJSON, agent string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &m); err != nil || m == nil {
		m = map[string]any{}
	}
	m["agent"] = agent
	return mustJSON(m)
}

func outcomeJSON(t Trajectory, success bool, agent string) string {
	var base map[string]any
	if err := json.Unmarshal([]byte(t.OutcomeJSON), &base); err != nil || base == nil {
		base = map[string]any{}
	}
	base["success"] = success
	base["tokens"] = t.Tokens
	base["toolCalls"] = t.ToolCalls
	base["toolErrors"] = t.ToolErrors
	base["latencyMs"] = t.LatencyMS
	base["agent"] = agent
	if t.CostUSD > 0 {
		base["costUsd"] = t.CostUSD
	}
	return mustJSON(base)
}
