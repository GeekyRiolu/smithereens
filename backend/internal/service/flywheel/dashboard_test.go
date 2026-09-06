package flywheel

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOverviewAssemblesEveryRecord(t *testing.T) {
	svc, _, proj := newSvc(t)
	ctx := context.Background()

	if _, err := svc.RunTriageDemo(ctx, proj); err != nil {
		t.Fatalf("run demo: %v", err)
	}
	ov, err := svc.Overview(ctx, proj)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}

	// The dual curve: 4 points, accuracy rising, cost falling, ablation gap.
	if len(ov.Cycles) != 4 {
		t.Fatalf("expected 4 cycle points, got %d", len(ov.Cycles))
	}
	first, last := ov.Cycles[0], ov.Cycles[3]
	if !(last.Metrics.Accuracy > first.Metrics.Accuracy) {
		t.Fatalf("accuracy did not rise across cycles: %.3f -> %.3f", first.Metrics.Accuracy, last.Metrics.Accuracy)
	}
	if !(last.Metrics.AvgTokens < first.Metrics.AvgTokens) {
		t.Fatalf("cost did not fall across cycles: %.0f -> %.0f", first.Metrics.AvgTokens, last.Metrics.AvgTokens)
	}
	if !(last.Metrics.Accuracy > last.AblationAccuracy) {
		t.Fatalf("expected an ablation gap at the last cycle: on=%.3f off=%.3f", last.Metrics.Accuracy, last.AblationAccuracy)
	}

	// The memory timeline: grew, with both knowledge and tool cards active.
	if ov.Memory.Active == 0 {
		t.Fatal("expected active memory")
	}
	if ov.Memory.ActiveByKind["knowledge"] == 0 || ov.Memory.ActiveByKind["tool_card"] == 0 {
		t.Fatalf("expected active knowledge + tool_card memory, got %+v", ov.Memory.ActiveByKind)
	}
	if ov.Memory.Quarantined == 0 {
		t.Fatal("expected some quarantined memory (the gate rejected useless lessons)")
	}

	// Every record is present for the frontend.
	if len(ov.RecentEpisodes) == 0 {
		t.Fatal("expected episodes")
	}
	if len(ov.RecentEvalRuns) == 0 {
		t.Fatal("expected eval runs")
	}

	// The whole overview must serialize cleanly (it is an API response).
	if _, err := json.Marshal(ov); err != nil {
		t.Fatalf("overview must be JSON-serializable: %v", err)
	}
}
