package flywheel

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TestTriageDemoImproves is the proof: running the loop makes the agent
// measurably better (accuracy up, cost down), driven only by memory that the
// eval gate promoted — with the memory-off ablation confirming the gain is
// causal. It also prints the curve so the improvement is visible.
func TestTriageDemoImproves(t *testing.T) {
	svc, s, proj := newSvc(t)
	ctx := context.Background()

	reports, err := svc.RunTriageDemo(ctx, proj)
	if err != nil {
		t.Fatalf("run demo: %v", err)
	}
	if len(reports) != 4 {
		t.Fatalf("expected 4 curve points (cold + 3 cycles), got %d", len(reports))
	}

	t.Log("cycle | accuracy | avgTokens | toolCalls | toolErrRate | promoted | quarantined | ablation(on-off)")
	for i, r := range reports {
		t.Logf("  %d   |  %.3f   |  %7.0f  |   %.2f    |    %.2f     |    %d     |     %d       |   %.3f",
			i, r.MemoryOn.Accuracy, r.MemoryOn.AvgTokens, r.MemoryOn.AvgToolCalls, r.MemoryOn.ToolErrorRate,
			r.Cycle.Promoted, r.Cycle.Quarantined, r.MemoryOn.Accuracy-r.MemoryOff.Accuracy)
	}

	cold, final := reports[0], reports[len(reports)-1]

	// Quality rose and cost fell — the dual curve.
	if !(final.MemoryOn.Accuracy > cold.MemoryOn.Accuracy) {
		t.Fatalf("accuracy did not improve: cold=%.3f final=%.3f", cold.MemoryOn.Accuracy, final.MemoryOn.Accuracy)
	}
	if !(final.MemoryOn.AvgTokens < cold.MemoryOn.AvgTokens) {
		t.Fatalf("cost did not fall: cold=%.0f final=%.0f", cold.MemoryOn.AvgTokens, final.MemoryOn.AvgTokens)
	}
	if final.MemoryOn.Accuracy < 0.999 {
		t.Fatalf("expected final accuracy ~1.0, got %.3f", final.MemoryOn.Accuracy)
	}

	// Accuracy is monotonically non-decreasing (no regressions between cycles).
	for i := 1; i < len(reports); i++ {
		if reports[i].MemoryOn.Accuracy < reports[i-1].MemoryOn.Accuracy-1e-9 {
			t.Fatalf("accuracy regressed at cycle %d: %.3f < %.3f", i, reports[i].MemoryOn.Accuracy, reports[i-1].MemoryOn.Accuracy)
		}
	}

	// The gain is causal: with memory off, the final suite collapses toward cold.
	if !(final.MemoryOn.Accuracy > final.MemoryOff.Accuracy+0.2) {
		t.Fatalf("ablation gap too small: on=%.3f off=%.3f", final.MemoryOn.Accuracy, final.MemoryOff.Accuracy)
	}

	// The gate actually gated: lessons were promoted AND at least one rejected.
	totalPromoted, totalQuarantined := 0, 0
	for _, r := range reports {
		totalPromoted += r.Cycle.Promoted
		totalQuarantined += r.Cycle.Quarantined
	}
	if totalPromoted == 0 {
		t.Fatal("expected some lessons promoted")
	}
	if totalQuarantined == 0 {
		t.Fatal("expected the eval gate to quarantine at least one useless lesson")
	}

	// Memory grew (active lessons exist) and the key contextual rule is present.
	active, err := s.ListFlywheelMemoryByStatus(ctx, proj, domain.MemoryActive)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) == 0 {
		t.Fatal("expected active memory to have grown")
	}
	foundEnterpriseP1 := false
	for _, m := range active {
		if m.Kind == domain.MemoryKnowledge && m.StructuredJSON != "" &&
			containsAll(m.StructuredJSON, `"tier":"enterprise"`, `"keyword":"dup_charge"`, `"priority":1`) {
			foundEnterpriseP1 = true
			// It should have been corroborated more than once (support rose).
			if m.SupportCount < 3 {
				t.Fatalf("expected corroborated support >= 3 on the key rule, got %d", m.SupportCount)
			}
		}
	}
	if !foundEnterpriseP1 {
		t.Fatal("expected the learned contextual rule (enterprise dup_charge -> P1) in active memory")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
