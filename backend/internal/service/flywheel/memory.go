// Package flywheel implements AO's self-improving agent runtime: the memory
// service (this file) plus, in later slices, reflection/consolidation and the
// eval-gated promotion that decide what the agent is allowed to remember.
//
// The memory service owns the four typed long-term memories (knowledge,
// playbook, tool_card, preference) layered on the episodic substrate. It is
// deliberately small and pure where it matters — retrieval ranking, budgeting,
// and consolidation merges are unit-testable functions — with persistence
// behind the Store interface (satisfied by the SQLite store).
package flywheel

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Store is the persistence surface the memory service needs. The concrete
// *sqlite.Store satisfies it.
type Store interface {
	CreateFlywheelEpisode(ctx context.Context, ep domain.FlywheelEpisode) (domain.FlywheelEpisode, error)
	ListUnreflectedFlywheelEpisodes(ctx context.Context, projectID string, limit int) ([]domain.FlywheelEpisode, error)
	MarkFlywheelEpisodeReflected(ctx context.Context, id string, reflectedAt time.Time) error

	CreateFlywheelMemory(ctx context.Context, m domain.FlywheelMemoryEntry) (domain.FlywheelMemoryEntry, error)
	GetFlywheelMemory(ctx context.Context, id string) (domain.FlywheelMemoryEntry, bool, error)
	ListFlywheelMemory(ctx context.Context, projectID string) ([]domain.FlywheelMemoryEntry, error)
	ListFlywheelMemoryByStatus(ctx context.Context, projectID string, status domain.MemoryStatus) ([]domain.FlywheelMemoryEntry, error)
	UpdateFlywheelMemoryStatus(ctx context.Context, id string, status domain.MemoryStatus, quarantineReason string, updatedAt time.Time) error
	UpdateFlywheelMemoryStats(ctx context.Context, id string, confidence float64, supportCount, contradictionCount int, lastConfirmedAt *time.Time, updatedAt time.Time) error
	MarkFlywheelMemoryUsed(ctx context.Context, id string, lastUsedAt time.Time) error

	CreateFlywheelEvalCase(ctx context.Context, c domain.FlywheelEvalCase) (domain.FlywheelEvalCase, error)
	ListFlywheelEvalCases(ctx context.Context, projectID string) ([]domain.FlywheelEvalCase, error)
	CreateFlywheelEvalRun(ctx context.Context, r domain.FlywheelEvalRun) (domain.FlywheelEvalRun, error)
	GetFlywheelEvalRun(ctx context.Context, id string) (domain.FlywheelEvalRun, bool, error)
	ListFlywheelEvalRuns(ctx context.Context, projectID string, limit int) ([]domain.FlywheelEvalRun, error)
	CreateFlywheelConsolidationCycle(ctx context.Context, c domain.FlywheelConsolidationCycle) (domain.FlywheelConsolidationCycle, error)
	ListFlywheelConsolidationCycles(ctx context.Context, projectID string) ([]domain.FlywheelConsolidationCycle, error)
	ListFlywheelEpisodes(ctx context.Context, projectID string, limit int) ([]domain.FlywheelEpisode, error)
}

// Service is the memory service.
type Service struct {
	store Store
	now   func() time.Time

	// live tracks the async live-demo run per project (real Claude Code agent
	// runs are long, so they run in the background and the UI polls Overview).
	mu   sync.Mutex
	live map[string]*liveRunState
}

// New constructs a memory service over the given store.
func New(store Store) *Service {
	return &Service{store: store, now: time.Now, live: map[string]*liveRunState{}}
}

// RecordEpisode persists one completed run into the episodic substrate.
func (s *Service) RecordEpisode(ctx context.Context, ep domain.FlywheelEpisode) (domain.FlywheelEpisode, error) {
	return s.store.CreateFlywheelEpisode(ctx, ep)
}

// Retrieve returns the highest-value active memory for a task, within a token
// budget. This is the fast loop's Retrieve stage: small and sharp beats a blob.
// budgetTokens <= 0 means no limit. Selected entries are marked used.
func (s *Service) Retrieve(ctx context.Context, projectID, taskType string, budgetTokens int) ([]domain.FlywheelMemoryEntry, error) {
	active, err := s.store.ListFlywheelMemoryByStatus(ctx, projectID, domain.MemoryActive)
	if err != nil {
		return nil, fmt.Errorf("retrieve active memory: %w", err)
	}
	now := s.now()

	// Filter to this task's scope (task-specific or project-global) and rank.
	scoped := make([]domain.FlywheelMemoryEntry, 0, len(active))
	for _, m := range active {
		if m.ScopeTask == "" || m.ScopeTask == taskType {
			scoped = append(scoped, m)
		}
	}
	sort.SliceStable(scoped, func(i, j int) bool {
		return scoreMemory(scoped[i], now) > scoreMemory(scoped[j], now)
	})

	selected := selectWithinBudget(scoped, budgetTokens)
	for _, m := range selected {
		// Best-effort: retrieval must not fail because a usage stamp did not write.
		_ = s.store.MarkFlywheelMemoryUsed(ctx, m.ID, now)
	}
	return selected, nil
}

// Propose records candidate lessons from a consolidation cycle. A candidate that
// restates an existing lesson (same kind+scope+title) is merged: its support
// count rises and confidence moves toward 1 (corroboration), rather than
// creating a duplicate. New candidates are stored with status=candidate for the
// eval gate to promote later. Returns the resulting entry ids (one per input).
func (s *Service) Propose(ctx context.Context, projectID string, candidates []domain.FlywheelMemoryEntry) ([]string, error) {
	existing, err := s.store.ListFlywheelMemory(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("load existing memory: %w", err)
	}
	byKey := make(map[string]domain.FlywheelMemoryEntry, len(existing))
	for _, m := range existing {
		key := mergeKey(m)
		// Prefer an active/candidate incumbent over a deprecated one.
		if cur, ok := byKey[key]; !ok || rank(m.Status) > rank(cur.Status) {
			byKey[key] = m
		}
	}

	now := s.now()
	ids := make([]string, 0, len(candidates))
	for _, cand := range candidates {
		cand.ProjectID = projectID
		if incumbent, ok := byKey[mergeKey(cand)]; ok && incumbent.Status != domain.MemoryQuarantined {
			support := incumbent.SupportCount + 1
			conf := corroborate(incumbent.Confidence)
			if err := s.store.UpdateFlywheelMemoryStats(ctx, incumbent.ID, conf, support, incumbent.ContradictionCount, &now, now); err != nil {
				return nil, fmt.Errorf("merge candidate %q: %w", cand.Title, err)
			}
			ids = append(ids, incumbent.ID)
			continue
		}
		cand.Status = domain.MemoryCandidate
		if cand.Confidence == 0 {
			cand.Confidence = 0.5
		}
		if cand.SupportCount == 0 {
			cand.SupportCount = 1
		}
		created, err := s.store.CreateFlywheelMemory(ctx, cand)
		if err != nil {
			return nil, fmt.Errorf("create candidate %q: %w", cand.Title, err)
		}
		byKey[mergeKey(created)] = created
		ids = append(ids, created.ID)
	}
	return ids, nil
}

// Promote activates a candidate lesson (the eval gate's positive verdict).
func (s *Service) Promote(ctx context.Context, id string) error {
	return s.store.UpdateFlywheelMemoryStatus(ctx, id, domain.MemoryActive, "", s.now())
}

// Quarantine sidelines a lesson that failed the eval gate or contradicts an
// active one. It is kept for audit and never retrieved.
func (s *Service) Quarantine(ctx context.Context, id, reason string) error {
	return s.store.UpdateFlywheelMemoryStatus(ctx, id, domain.MemoryQuarantined, reason, s.now())
}

// Decay deprecates active lessons that have not been confirmed within maxAge, so
// stale knowledge stops being retrieved instead of accumulating. Returns the ids
// deprecated.
func (s *Service) Decay(ctx context.Context, projectID string, maxAge time.Duration) ([]string, error) {
	active, err := s.store.ListFlywheelMemoryByStatus(ctx, projectID, domain.MemoryActive)
	if err != nil {
		return nil, fmt.Errorf("decay: load active: %w", err)
	}
	now := s.now()
	deprecated := []string{}
	for _, m := range active {
		ref := m.UpdatedAt
		if m.LastConfirmedAt != nil {
			ref = *m.LastConfirmedAt
		}
		if now.Sub(ref) > maxAge {
			if err := s.store.UpdateFlywheelMemoryStatus(ctx, m.ID, domain.MemoryDeprecated, "decayed: not confirmed within retention window", now); err != nil {
				return nil, fmt.Errorf("decay %q: %w", m.ID, err)
			}
			deprecated = append(deprecated, m.ID)
		}
	}
	return deprecated, nil
}

// ---- pure helpers (unit-tested directly) ------------------------------------

// scoreMemory ranks a memory for retrieval: confidence, weighted by support
// (corroboration) and recency (freshness). All factors are in [0,1].
func scoreMemory(m domain.FlywheelMemoryEntry, now time.Time) float64 {
	support := math.Min(1, float64(m.SupportCount)/10.0)
	supportWeight := 0.5 + 0.5*support

	ref := m.UpdatedAt
	if m.LastConfirmedAt != nil {
		ref = *m.LastConfirmedAt
	}
	ageDays := now.Sub(ref).Hours() / 24.0
	if ageDays < 0 {
		ageDays = 0
	}
	const halfLifeDays = 30.0
	recencyWeight := math.Exp(-ageDays / halfLifeDays)

	return m.Confidence * supportWeight * recencyWeight
}

// estimateTokens is a cheap char/4 approximation of a memory's context cost.
func estimateTokens(m domain.FlywheelMemoryEntry) int {
	chars := len(m.Title) + len(m.Body) + len(m.StructuredJSON)
	return chars/4 + 8 // small per-entry framing overhead
}

// selectWithinBudget greedily takes entries (already ranked) until the token
// budget is exhausted, always returning at least the top entry when budget > 0.
// A budget <= 0 means no limit.
func selectWithinBudget(ranked []domain.FlywheelMemoryEntry, budgetTokens int) []domain.FlywheelMemoryEntry {
	if budgetTokens <= 0 {
		out := make([]domain.FlywheelMemoryEntry, len(ranked))
		copy(out, ranked)
		return out
	}
	out := make([]domain.FlywheelMemoryEntry, 0, len(ranked))
	used := 0
	for _, m := range ranked {
		cost := estimateTokens(m)
		if len(out) > 0 && used+cost > budgetTokens {
			break
		}
		out = append(out, m)
		used += cost
		if used >= budgetTokens {
			break
		}
	}
	return out
}

// mergeKey identifies "the same lesson" for consolidation dedup.
func mergeKey(m domain.FlywheelMemoryEntry) string {
	return string(m.Kind) + "|" + m.ScopeTask + "|" + m.ScopeTool + "|" + normalizeTitle(m.Title)
}

func normalizeTitle(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// corroborate moves confidence a third of the way toward certainty each time a
// lesson is independently restated.
func corroborate(c float64) float64 {
	c += (1 - c) * 0.34
	if c > 0.99 {
		c = 0.99
	}
	return c
}

// rank orders statuses so a live incumbent wins dedup over a deprecated one.
func rank(s domain.MemoryStatus) int {
	switch s {
	case domain.MemoryActive:
		return 3
	case domain.MemoryCandidate:
		return 2
	case domain.MemoryQuarantined:
		return 1
	default: // deprecated
		return 0
	}
}
