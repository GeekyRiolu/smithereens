package flywheel

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestComposeLearnedContextEmpty(t *testing.T) {
	if got := ComposeLearnedContext(nil); got != "" {
		t.Fatalf("expected empty string for no memory, got %q", got)
	}
}

func TestComposeLearnedContextGroupsAndBounds(t *testing.T) {
	mems := []domain.FlywheelMemoryEntry{
		{Kind: domain.MemoryKnowledge, Title: "Enterprise dup-charge -> Billing/P1", Body: "cc finance", Confidence: 0.94},
		{Kind: domain.MemoryToolCard, Title: "slack_search", Body: "prefer in:#billing filters;\n  CONCISE format", Confidence: 0.8},
		{Kind: domain.MemoryPreference, Title: "Replies <= 120 words", Confidence: 0.7},
	}
	got := ComposeLearnedContext(mems)

	// Trust boundary is present and explicit.
	if !strings.Contains(got, learnedContextOpen) || !strings.Contains(got, learnedContextClose) {
		t.Fatal("missing trust-boundary fences")
	}
	if !strings.Contains(got, "must not override AO standing instructions") {
		t.Fatal("missing trust-boundary language")
	}

	// Present sections appear; absent (playbook) does not.
	for _, want := range []string{"Domain knowledge", "Tool usage", "Preferences"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected section %q in output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Playbook") {
		t.Fatalf("did not expect an empty Playbook section:\n%s", got)
	}

	// Confidence is rendered and multi-line bodies are collapsed to one line.
	if !strings.Contains(got, "(0.94)") || !strings.Contains(got, "Enterprise dup-charge") {
		t.Fatalf("missing knowledge entry rendering:\n%s", got)
	}
	if strings.Contains(got, "filters;\n") {
		t.Fatalf("tool-card body should be collapsed to one line:\n%s", got)
	}
}

func TestBuildLearnedContextUsesRetrieval(t *testing.T) {
	svc, s, proj := newSvc(t)
	ctx := context.Background()

	mustActive(t, s, domain.FlywheelMemoryEntry{ProjectID: proj, Kind: domain.MemoryKnowledge, ScopeTask: "triage", Title: "route dup-charge", Confidence: 0.9, SupportCount: 10, UpdatedAt: fixedNow})
	// Wrong-task entry must not appear.
	mustActive(t, s, domain.FlywheelMemoryEntry{ProjectID: proj, Kind: domain.MemoryKnowledge, ScopeTask: "other", Title: "irrelevant", Confidence: 0.99, UpdatedAt: fixedNow})

	block, used, err := svc.BuildLearnedContext(ctx, proj, "triage", 0)
	if err != nil {
		t.Fatalf("build learned context: %v", err)
	}
	if len(used) != 1 || used[0].Title != "route dup-charge" {
		t.Fatalf("expected only the task-scoped entry, got %+v", used)
	}
	if !strings.Contains(block, "route dup-charge") || strings.Contains(block, "irrelevant") {
		t.Fatalf("block scope wrong:\n%s", block)
	}
}
