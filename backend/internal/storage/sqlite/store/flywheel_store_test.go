package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func within(a, b time.Time) bool {
	d := a.Sub(b)
	return d < time.Second && d > -time.Second
}

func TestFlywheelEpisodeRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "fw")

	// Blank JSON fields must default to valid JSON (json_valid CHECK).
	ep, err := s.CreateFlywheelEpisode(ctx, domain.FlywheelEpisode{
		ProjectID: "fw", SessionID: "sess-1", TaskType: "support_triage",
		OutcomeJSON: `{"success":true,"tokens":1200}`,
	})
	if err != nil {
		t.Fatalf("create episode: %v", err)
	}
	if ep.ID == "" || ep.CreatedAt.IsZero() {
		t.Fatalf("expected minted id and created_at, got %+v", ep)
	}
	if ep.InputJSON != "{}" || ep.RetrievedJSON != "[]" || ep.TraceJSON != "[]" || ep.FeedbackJSON != "{}" {
		t.Fatalf("blank JSON fields not defaulted: %+v", ep)
	}

	got, ok, err := s.GetFlywheelEpisode(ctx, ep.ID)
	if err != nil || !ok {
		t.Fatalf("get episode = %v, ok=%v", err, ok)
	}
	if got.TaskType != "support_triage" || got.OutcomeJSON != `{"success":true,"tokens":1200}` {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.ReflectedAt != nil {
		t.Fatalf("expected nil ReflectedAt, got %v", got.ReflectedAt)
	}
}

func TestFlywheelUnreflectedFilterAndMark(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "fw")

	a, _ := s.CreateFlywheelEpisode(ctx, domain.FlywheelEpisode{ProjectID: "fw", TaskType: "t"})
	_, _ = s.CreateFlywheelEpisode(ctx, domain.FlywheelEpisode{ProjectID: "fw", TaskType: "t"})

	un, err := s.ListUnreflectedFlywheelEpisodes(ctx, "fw", 10)
	if err != nil {
		t.Fatalf("list unreflected: %v", err)
	}
	if len(un) != 2 {
		t.Fatalf("expected 2 unreflected, got %d", len(un))
	}

	if err := s.MarkFlywheelEpisodeReflected(ctx, a.ID, time.Now().UTC()); err != nil {
		t.Fatalf("mark reflected: %v", err)
	}
	un, _ = s.ListUnreflectedFlywheelEpisodes(ctx, "fw", 10)
	if len(un) != 1 {
		t.Fatalf("expected 1 unreflected after mark, got %d", len(un))
	}

	got, _, _ := s.GetFlywheelEpisode(ctx, a.ID)
	if got.ReflectedAt == nil {
		t.Fatalf("expected ReflectedAt set after mark")
	}
}

func TestFlywheelMemoryLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "fw")

	// Invalid kind/status are rejected before hitting the CHECK constraint.
	if _, err := s.CreateFlywheelMemory(ctx, domain.FlywheelMemoryEntry{ProjectID: "fw", Kind: "bogus", Status: domain.MemoryCandidate, Title: "x"}); err == nil {
		t.Fatal("expected error for invalid kind")
	}
	if _, err := s.CreateFlywheelMemory(ctx, domain.FlywheelMemoryEntry{ProjectID: "fw", Kind: domain.MemoryKnowledge, Status: "bogus", Title: "x"}); err == nil {
		t.Fatal("expected error for invalid status")
	}

	m, err := s.CreateFlywheelMemory(ctx, domain.FlywheelMemoryEntry{
		ProjectID: "fw", Kind: domain.MemoryKnowledge, Status: domain.MemoryCandidate,
		ScopeTask: "support_triage", Title: "Enterprise dup-charge routing",
		Body: "route to Billing/P1, CC finance", Confidence: 0.7, SupportCount: 3,
		StructuredJSON: `{"action":"route=Billing/P1"}`, ProvenanceJSON: `["ep_1","ep_2"]`,
	})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}
	if m.ID == "" || m.Version != 1 {
		t.Fatalf("expected minted id and version 1, got %+v", m)
	}

	// Only 'active' is retrievable; candidate should not appear in active list.
	if act, _ := s.ListFlywheelMemoryByStatus(ctx, "fw", domain.MemoryActive); len(act) != 0 {
		t.Fatalf("expected 0 active, got %d", len(act))
	}

	// Promote and confirm it becomes retrievable + version bumps.
	if err := s.UpdateFlywheelMemoryStatus(ctx, m.ID, domain.MemoryActive, "", time.Now().UTC()); err != nil {
		t.Fatalf("promote: %v", err)
	}
	act, _ := s.ListFlywheelMemoryByStatus(ctx, "fw", domain.MemoryActive)
	if len(act) != 1 || act[0].Status != domain.MemoryActive || act[0].Version != 2 {
		t.Fatalf("expected 1 active v2, got %+v", act)
	}

	// Consolidation raises confidence + support.
	confirmed := time.Now().UTC()
	if err := s.UpdateFlywheelMemoryStats(ctx, m.ID, 0.94, 41, 0, &confirmed, time.Now().UTC()); err != nil {
		t.Fatalf("update stats: %v", err)
	}
	got, _, _ := s.GetFlywheelMemory(ctx, m.ID)
	if got.Confidence != 0.94 || got.SupportCount != 41 || got.LastConfirmedAt == nil {
		t.Fatalf("stats not persisted: %+v", got)
	}

	// Mark used.
	used := time.Now().UTC()
	if err := s.MarkFlywheelMemoryUsed(ctx, m.ID, used); err != nil {
		t.Fatalf("mark used: %v", err)
	}
	got, _, _ = s.GetFlywheelMemory(ctx, m.ID)
	if got.LastUsedAt == nil || !within(*got.LastUsedAt, used) {
		t.Fatalf("last_used not persisted: %+v", got.LastUsedAt)
	}
}

func TestFlywheelMemoryActiveOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "fw")

	mk := func(title string, conf float64) {
		if _, err := s.CreateFlywheelMemory(ctx, domain.FlywheelMemoryEntry{
			ProjectID: "fw", Kind: domain.MemoryToolCard, Status: domain.MemoryActive,
			Title: title, Confidence: conf, SupportCount: 1,
		}); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
	}
	mk("low", 0.3)
	mk("high", 0.95)
	mk("mid", 0.6)

	act, err := s.ListFlywheelMemoryByStatus(ctx, "fw", domain.MemoryActive)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(act) != 3 || act[0].Title != "high" || act[2].Title != "low" {
		t.Fatalf("expected confidence-desc order high..low, got %v", []string{act[0].Title, act[1].Title, act[2].Title})
	}
}

func TestFlywheelEvalCasesRunsAndCycles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "fw")

	if _, err := s.CreateFlywheelEvalCase(ctx, domain.FlywheelEvalCase{ProjectID: "fw", TaskType: "t", Kind: "bogus", Polarity: domain.EvalPositive}); err == nil {
		t.Fatal("expected error for invalid eval kind")
	}
	if _, err := s.CreateFlywheelEvalCase(ctx, domain.FlywheelEvalCase{ProjectID: "fw", TaskType: "t", Kind: domain.EvalCapability, Polarity: "bogus"}); err == nil {
		t.Fatal("expected error for invalid polarity")
	}

	c, err := s.CreateFlywheelEvalCase(ctx, domain.FlywheelEvalCase{
		ProjectID: "fw", TaskType: "support_triage", Kind: domain.EvalCapability,
		Polarity: domain.EvalPositive, GradersJSON: `[{"type":"code"}]`,
	})
	if err != nil {
		t.Fatalf("create eval case: %v", err)
	}
	cases, _ := s.ListFlywheelEvalCases(ctx, "fw")
	if len(cases) != 1 || cases[0].ID != c.ID || cases[0].GradersJSON != `[{"type":"code"}]` {
		t.Fatalf("eval case round-trip mismatch: %+v", cases)
	}

	run, err := s.CreateFlywheelEvalRun(ctx, domain.FlywheelEvalRun{
		ProjectID: "fw", Ablation: "memory_on", MetricsJSON: `{"passK":0.78}`,
	})
	if err != nil {
		t.Fatalf("create eval run: %v", err)
	}
	gotRun, ok, _ := s.GetFlywheelEvalRun(ctx, run.ID)
	if !ok || gotRun.MetricsJSON != `{"passK":0.78}` || gotRun.Ablation != "memory_on" {
		t.Fatalf("eval run round-trip mismatch: %+v", gotRun)
	}
	if runs, _ := s.ListFlywheelEvalRuns(ctx, "fw", 10); len(runs) != 1 {
		t.Fatalf("expected 1 eval run, got %d", len(runs))
	}

	cyc, err := s.CreateFlywheelConsolidationCycle(ctx, domain.FlywheelConsolidationCycle{
		ProjectID: "fw", EpisodesSeen: 40, Candidates: 9, Promoted: 7, Quarantined: 2,
		EvalRunID: run.ID, NetDeltaJSON: `{"dPassK":0.19}`,
	})
	if err != nil {
		t.Fatalf("create cycle: %v", err)
	}
	cycles, _ := s.ListFlywheelConsolidationCycles(ctx, "fw")
	if len(cycles) != 1 || cycles[0].ID != cyc.ID || cycles[0].Promoted != 7 || cycles[0].EpisodesSeen != 40 {
		t.Fatalf("cycle round-trip mismatch: %+v", cycles)
	}
}
