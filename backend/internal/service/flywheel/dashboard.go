package flywheel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Dashboard assembly: one read that gives the renderer everything it needs to
// make the whole loop visible — the dual curve (per cycle), the memory timeline
// (grouped by status/kind), recent episodes, and eval runs. The HTTP controller
// is a thin wrapper over Overview.

// DashboardOverview is the full read model for the Learning tab.
type DashboardOverview struct {
	ProjectID      string                   `json:"projectId"`
	Cycles         []CyclePoint             `json:"cycles"`
	Memory         MemorySummary            `json:"memory"`
	RecentEpisodes []domain.FlywheelEpisode `json:"recentEpisodes"`
	RecentEvalRuns []domain.FlywheelEvalRun `json:"recentEvalRuns"`
	// LiveAvailable is true when the real `claude` CLI is present, so the live
	// demo can run. LiveRunning/LiveStep/LiveError report an in-flight live run.
	LiveAvailable bool   `json:"liveAvailable"`
	LiveRunning   bool   `json:"liveRunning"`
	LiveStep      string `json:"liveStep,omitempty"`
	LiveError     string `json:"liveError,omitempty"`
}

// CyclePoint is one point on the dual curve: a consolidation cycle plus its
// memory-on metrics and the memory-off ablation accuracy.
type CyclePoint struct {
	ID               string      `json:"id"`
	CreatedAt        time.Time   `json:"createdAt"`
	EpisodesSeen     int         `json:"episodesSeen"`
	Candidates       int         `json:"candidates"`
	Promoted         int         `json:"promoted"`
	Deprecated       int         `json:"deprecated"`
	Quarantined      int         `json:"quarantined"`
	Metrics          EvalMetrics `json:"metrics"`          // memory-on
	AblationAccuracy float64     `json:"ablationAccuracy"` // memory-off
}

// MemorySummary is the memory timeline: counts by status, active counts by kind,
// and every entry (candidate/active/deprecated/quarantined) for the feed.
type MemorySummary struct {
	Active       int                          `json:"active"`
	Candidate    int                          `json:"candidate"`
	Deprecated   int                          `json:"deprecated"`
	Quarantined  int                          `json:"quarantined"`
	ActiveByKind map[string]int               `json:"activeByKind"`
	Entries      []domain.FlywheelMemoryEntry `json:"entries"`
}

// Overview assembles the full dashboard read model for a project.
func (s *Service) Overview(ctx context.Context, projectID string) (DashboardOverview, error) {
	cycles, err := s.store.ListFlywheelConsolidationCycles(ctx, projectID)
	if err != nil {
		return DashboardOverview{}, fmt.Errorf("overview cycles: %w", err)
	}
	points := make([]CyclePoint, 0, len(cycles))
	for _, c := range cycles {
		p := CyclePoint{
			ID: c.ID, CreatedAt: c.CreatedAt, EpisodesSeen: c.EpisodesSeen,
			Candidates: c.Candidates, Promoted: c.Promoted, Deprecated: c.Deprecated,
			Quarantined: c.Quarantined,
		}
		if c.EvalRunID != "" {
			if run, ok, err := s.store.GetFlywheelEvalRun(ctx, c.EvalRunID); err == nil && ok {
				p.Metrics = parseEvalMetrics(run.MetricsJSON)
			}
		}
		// memory-off accuracy = memory-on minus the recorded ablation delta.
		if d, ok := parseNetDelta(c.NetDeltaJSON)["dAccuracy"]; ok {
			p.AblationAccuracy = p.Metrics.Accuracy - d
		} else {
			p.AblationAccuracy = p.Metrics.Accuracy
		}
		points = append(points, p)
	}

	entries, err := s.store.ListFlywheelMemory(ctx, projectID)
	if err != nil {
		return DashboardOverview{}, fmt.Errorf("overview memory: %w", err)
	}
	mem := MemorySummary{ActiveByKind: map[string]int{}, Entries: entries}
	for _, m := range entries {
		switch m.Status {
		case domain.MemoryActive:
			mem.Active++
			mem.ActiveByKind[string(m.Kind)]++
		case domain.MemoryCandidate:
			mem.Candidate++
		case domain.MemoryDeprecated:
			mem.Deprecated++
		case domain.MemoryQuarantined:
			mem.Quarantined++
		}
	}

	episodes, err := s.store.ListFlywheelEpisodes(ctx, projectID, 100)
	if err != nil {
		return DashboardOverview{}, fmt.Errorf("overview episodes: %w", err)
	}
	runs, err := s.store.ListFlywheelEvalRuns(ctx, projectID, 50)
	if err != nil {
		return DashboardOverview{}, fmt.Errorf("overview eval runs: %w", err)
	}

	live := s.liveStatus(projectID)
	return DashboardOverview{
		ProjectID: projectID, Cycles: points, Memory: mem,
		RecentEpisodes: episodes, RecentEvalRuns: runs,
		LiveAvailable: LiveAvailable(), LiveRunning: live.Running,
		LiveStep: live.Step, LiveError: live.Error,
	}, nil
}

func parseEvalMetrics(s string) EvalMetrics {
	var m EvalMetrics
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func parseNetDelta(s string) map[string]float64 {
	m := map[string]float64{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}
