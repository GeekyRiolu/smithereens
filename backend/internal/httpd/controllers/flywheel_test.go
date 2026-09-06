package controllers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/flywheel"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// TestFlywheelControllerEndToEnd drives the real HTTP path (router -> controller
// -> service -> SQLite): POST the demo, then confirm the returned dashboard
// shows the agent measurably improving, and that a follow-up GET reads the
// persisted result.
func TestFlywheelControllerEndToEnd(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "p1", Path: "/tmp/p1", RegisteredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	ctrl := &controllers.FlywheelController{Svc: flywheel.New(store)}
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { ctrl.Register(r) })
	ts := httptest.NewServer(r)
	defer ts.Close()

	// POST the demo and decode the returned dashboard.
	post, err := http.Post(ts.URL+"/api/v1/projects/p1/flywheel/demo", "application/json", nil)
	if err != nil {
		t.Fatalf("POST demo: %v", err)
	}
	defer func() { _ = post.Body.Close() }()
	if post.StatusCode != http.StatusOK {
		t.Fatalf("demo status = %d, want 200", post.StatusCode)
	}
	var ov flywheel.DashboardOverview
	if err := json.NewDecoder(post.Body).Decode(&ov); err != nil {
		t.Fatalf("decode demo overview: %v", err)
	}

	if len(ov.Cycles) != 4 {
		t.Fatalf("expected 4 cycle points, got %d", len(ov.Cycles))
	}
	first, last := ov.Cycles[0], ov.Cycles[3]
	if !(last.Metrics.Accuracy > first.Metrics.Accuracy) {
		t.Fatalf("accuracy did not improve over HTTP: %.3f -> %.3f", first.Metrics.Accuracy, last.Metrics.Accuracy)
	}
	if last.Metrics.Accuracy < 0.999 {
		t.Fatalf("expected final accuracy ~1.0, got %.3f", last.Metrics.Accuracy)
	}
	if !(last.Metrics.Accuracy > last.AblationAccuracy) {
		t.Fatalf("expected ablation gap: on=%.3f off=%.3f", last.Metrics.Accuracy, last.AblationAccuracy)
	}
	if ov.Memory.Active == 0 {
		t.Fatal("expected active memory after the demo")
	}

	// GET reads back the persisted dashboard.
	get, err := http.Get(ts.URL + "/api/v1/projects/p1/flywheel/overview")
	if err != nil {
		t.Fatalf("GET overview: %v", err)
	}
	defer func() { _ = get.Body.Close() }()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("overview status = %d, want 200", get.StatusCode)
	}
	var got flywheel.DashboardOverview
	if err := json.NewDecoder(get.Body).Decode(&got); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(got.Cycles) != 4 {
		t.Fatalf("persisted overview should have 4 cycles, got %d", len(got.Cycles))
	}
}

// TestFlywheelControllerNilServiceReturns501 confirms the surface degrades
// gracefully when the service is not wired.
func TestFlywheelControllerNilServiceReturns501(t *testing.T) {
	ctrl := &controllers.FlywheelController{Svc: nil}
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { ctrl.Register(r) })
	ts := httptest.NewServer(r)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/projects/p1/flywheel/overview")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("nil service status = %d, want 501", resp.StatusCode)
	}
}
