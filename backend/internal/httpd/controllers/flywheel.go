package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/flywheel"
)

// FlywheelOverviewResponse is the full Learning-tab read model. It aliases the
// service type so the wire shape stays defined in one place.
type FlywheelOverviewResponse = flywheel.DashboardOverview

// FlywheelProjectParam is the {projectId} path parameter for the Flywheel routes.
type FlywheelProjectParam struct {
	ProjectID string `path:"projectId" description:"Project id the Flywheel data belongs to."`
}

// FlywheelService is the read + demo surface the Learning tab needs. It is
// satisfied by *flywheel.Service. Read-only plus one trigger; consolidation and
// promotion policy stay in the service.
type FlywheelService interface {
	Overview(ctx context.Context, projectID string) (flywheel.DashboardOverview, error)
	RunTriageDemo(ctx context.Context, projectID string) ([]flywheel.CycleReport, error)
}

// FlywheelController serves the loopback Flywheel API. A nil Svc answers 501 so
// the surface degrades gracefully when the service is not wired.
type FlywheelController struct {
	Svc FlywheelService
}

// Register mounts the project-scoped Flywheel routes under /api/v1.
func (c *FlywheelController) Register(r chi.Router) {
	r.Get("/projects/{projectId}/flywheel/overview", c.overview)
	r.Post("/projects/{projectId}/flywheel/demo", c.runDemo)
}

// overview returns the full Learning-tab read model for a project.
func (c *FlywheelController) overview(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/projects/{projectId}/flywheel/overview")
		return
	}
	ov, err := c.Svc.Overview(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, FlywheelOverviewResponse(ov))
}

// runDemo runs the deterministic learning demo for a project (cold baseline +
// consolidation cycles across agents), then returns the updated overview so the
// caller can render the improvement in one round-trip.
func (c *FlywheelController) runDemo(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/projects/{projectId}/flywheel/demo")
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if _, err := c.Svc.RunTriageDemo(r.Context(), projectID); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	ov, err := c.Svc.Overview(r.Context(), projectID)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, FlywheelOverviewResponse(ov))
}
