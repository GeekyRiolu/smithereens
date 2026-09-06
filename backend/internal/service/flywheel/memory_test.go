package flywheel

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

var fixedNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func newSvc(t *testing.T) (*Service, *sqlite.Store, string) {
	t.Helper()
	s := sqlitetest.MustOpen(t)
	if err := s.UpsertProject(context.Background(), domain.ProjectRecord{
		ID: "fw", Path: "/tmp/fw", RegisteredAt: fixedNow,
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	svc := New(s)
	svc.now = func() time.Time { return fixedNow }
	return svc, s, "fw"
}

func mustActive(t *testing.T, s *sqlite.Store, m domain.FlywheelMemoryEntry) domain.FlywheelMemoryEntry {
	t.Helper()
	if m.Status == "" {
		m.Status = domain.MemoryActive
	}
	if m.Kind == "" {
		m.Kind = domain.MemoryKnowledge
	}
	out, err := s.CreateFlywheelMemory(context.Background(), m)
	if err != nil {
		t.Fatalf("create memory %q: %v", m.Title, err)
	}
	return out
}

func TestScoreMemoryFactors(t *testing.T) {
	base := domain.FlywheelMemoryEntry{Confidence: 0.9, SupportCount: 10, UpdatedAt: fixedNow}
	lowSupport := base
	lowSupport.SupportCount = 1
	stale := base
	old := fixedNow.Add(-60 * 24 * time.Hour)
	stale.UpdatedAt = old

	if scoreMemory(base, fixedNow) <= scoreMemory(lowSupport, fixedNow) {
		t.Fatal("more support should score higher")
	}
	if scoreMemory(base, fixedNow) <= scoreMemory(stale, fixedNow) {
		t.Fatal("fresher should score higher")
	}
	lowConf := base
	lowConf.Confidence = 0.3
	if scoreMemory(base, fixedNow) <= scoreMemory(lowConf, fixedNow) {
		t.Fatal("higher confidence should score higher")
	}
}

func TestSelectWithinBudget(t *testing.T) {
	big := domain.FlywheelMemoryEntry{Title: "a", Body: string(make([]byte, 400))} // ~100+ tokens
	small := domain.FlywheelMemoryEntry{Title: "b", Body: "short"}                 // ~small
	ranked := []domain.FlywheelMemoryEntry{big, small}

	if got := selectWithinBudget(ranked, 0); len(got) != 2 {
		t.Fatalf("budget<=0 means no limit, got %d", len(got))
	}
	// A tiny budget still returns at least the top entry.
	if got := selectWithinBudget(ranked, 5); len(got) != 1 || got[0].Title != "a" {
		t.Fatalf("expected only top entry under tiny budget, got %d", len(got))
	}
	// A budget big enough for both returns both.
	if got := selectWithinBudget(ranked, 1000); len(got) != 2 {
		t.Fatalf("expected both under large budget, got %d", len(got))
	}
}

func TestRetrieveScopeRankingAndMarkUsed(t *testing.T) {
	svc, s, proj := newSvc(t)
	ctx := context.Background()

	top := mustActive(t, s, domain.FlywheelMemoryEntry{ProjectID: proj, ScopeTask: "triage", Title: "high", Confidence: 0.95, SupportCount: 20, UpdatedAt: fixedNow})
	mustActive(t, s, domain.FlywheelMemoryEntry{ProjectID: proj, ScopeTask: "", Title: "global-mid", Confidence: 0.6, SupportCount: 1, UpdatedAt: fixedNow})
	mustActive(t, s, domain.FlywheelMemoryEntry{ProjectID: proj, ScopeTask: "other", Title: "wrong-task", Confidence: 0.99, SupportCount: 50, UpdatedAt: fixedNow})
	// A candidate (not active) must never be retrieved.
	if _, err := s.CreateFlywheelMemory(ctx, domain.FlywheelMemoryEntry{ProjectID: proj, Kind: domain.MemoryKnowledge, Status: domain.MemoryCandidate, ScopeTask: "triage", Title: "candidate"}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Retrieve(ctx, proj, "triage", 0)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 (task + global), got %d: %+v", len(got), titles(got))
	}
	if got[0].Title != "high" {
		t.Fatalf("expected 'high' ranked first, got %v", titles(got))
	}

	// Retrieval stamps last_used_at on selected entries.
	reloaded, _, _ := s.GetFlywheelMemory(ctx, top.ID)
	if reloaded.LastUsedAt == nil {
		t.Fatal("expected last_used_at set after retrieve")
	}
}

func TestProposeMergesRestatedLesson(t *testing.T) {
	svc, s, proj := newSvc(t)
	ctx := context.Background()

	cand := domain.FlywheelMemoryEntry{
		Kind: domain.MemoryKnowledge, ScopeTask: "triage",
		Title: "Route Enterprise dup-charge to Billing/P1",
		Body:  "cc finance", Confidence: 0.5,
	}
	ids1, err := svc.Propose(ctx, proj, []domain.FlywheelMemoryEntry{cand})
	if err != nil || len(ids1) != 1 {
		t.Fatalf("first propose: ids=%v err=%v", ids1, err)
	}

	// Restating the same lesson (title differs only in case/spacing) merges.
	restated := cand
	restated.Title = "  route enterprise DUP-charge to billing/p1  "
	ids2, err := svc.Propose(ctx, proj, []domain.FlywheelMemoryEntry{restated})
	if err != nil || len(ids2) != 1 {
		t.Fatalf("second propose: ids=%v err=%v", ids2, err)
	}
	if ids2[0] != ids1[0] {
		t.Fatalf("expected merge into same entry, got %s vs %s", ids2[0], ids1[0])
	}

	all, _ := s.ListFlywheelMemory(ctx, proj)
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 entry after merge, got %d", len(all))
	}
	if all[0].SupportCount != 2 {
		t.Fatalf("expected support_count 2 after corroboration, got %d", all[0].SupportCount)
	}
	if all[0].Confidence <= 0.5 {
		t.Fatalf("expected confidence to rise after corroboration, got %v", all[0].Confidence)
	}
}

func TestPromoteAndQuarantine(t *testing.T) {
	svc, s, proj := newSvc(t)
	ctx := context.Background()

	cand, _ := s.CreateFlywheelMemory(ctx, domain.FlywheelMemoryEntry{ProjectID: proj, Kind: domain.MemoryPlaybook, Status: domain.MemoryCandidate, Title: "p"})
	if err := svc.Promote(ctx, cand.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	got, _, _ := s.GetFlywheelMemory(ctx, cand.ID)
	if got.Status != domain.MemoryActive {
		t.Fatalf("expected active, got %s", got.Status)
	}

	bad, _ := s.CreateFlywheelMemory(ctx, domain.FlywheelMemoryEntry{ProjectID: proj, Kind: domain.MemoryPlaybook, Status: domain.MemoryCandidate, Title: "q"})
	if err := svc.Quarantine(ctx, bad.ID, "regressed the suite"); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	got, _, _ = s.GetFlywheelMemory(ctx, bad.ID)
	if got.Status != domain.MemoryQuarantined || got.QuarantineReason != "regressed the suite" {
		t.Fatalf("expected quarantined with reason, got %s / %q", got.Status, got.QuarantineReason)
	}
}

func TestDecayDeprecatesStale(t *testing.T) {
	svc, s, proj := newSvc(t)
	ctx := context.Background()

	old := fixedNow.Add(-60 * 24 * time.Hour)
	recent := fixedNow.Add(-24 * time.Hour)
	stale := mustActive(t, s, domain.FlywheelMemoryEntry{ProjectID: proj, Title: "stale", UpdatedAt: old, LastConfirmedAt: &old})
	fresh := mustActive(t, s, domain.FlywheelMemoryEntry{ProjectID: proj, Title: "fresh", UpdatedAt: recent, LastConfirmedAt: &recent})

	dep, err := svc.Decay(ctx, proj, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("decay: %v", err)
	}
	if len(dep) != 1 || dep[0] != stale.ID {
		t.Fatalf("expected only stale deprecated, got %v", dep)
	}
	gotStale, _, _ := s.GetFlywheelMemory(ctx, stale.ID)
	gotFresh, _, _ := s.GetFlywheelMemory(ctx, fresh.ID)
	if gotStale.Status != domain.MemoryDeprecated {
		t.Fatalf("expected stale deprecated, got %s", gotStale.Status)
	}
	if gotFresh.Status != domain.MemoryActive {
		t.Fatalf("expected fresh retained active, got %s", gotFresh.Status)
	}
}

func titles(ms []domain.FlywheelMemoryEntry) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Title
	}
	return out
}
