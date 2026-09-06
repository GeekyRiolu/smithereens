package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Flywheel persistence (see migration 0127_flywheel.sql). This store is
// hand-written against database/sql rather than sqlc-generated: the repo's
// committed gen/ predates the pinned sqlc version and regenerating reshapes
// unrelated query return types, so Flywheel stays isolated from that drift.
// queries/flywheel.sql documents the equivalent sqlc queries for when the
// repo's codegen is reconciled.

const (
	fwEpisodeCols  = "id, project_id, session_id, task_type, input_json, retrieved_json, trace_json, outcome_json, feedback_json, reflected_at, created_at"
	fwMemoryCols   = "id, project_id, kind, scope_task, scope_tool, title, body, structured_json, confidence, support_count, contradiction_count, status, version, provenance_json, eval_refs_json, quarantine_reason, created_at, updated_at, last_used_at, last_confirmed_at"
	fwEvalCaseCols = "id, project_id, task_type, kind, polarity, input_json, reference_json, graders_json, fixture_ref, source_episode_id, created_at"
	fwEvalRunCols  = "id, project_id, cycle_id, memory_version, staged_memory_id, ablation, metrics_json, per_case_json, created_at"
	fwCycleCols    = "id, project_id, episodes_seen, candidates, promoted, deprecated, quarantined, eval_run_id, net_delta_json, created_at"
)

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// newFlywheelID mints a short, prefixed, sortable-enough id (prefix_<16 hex>).
func newFlywheelID(prefix string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand should never fail; fall back to a time-derived id.
		return fmt.Sprintf("%s_%x", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf[:])
}

// fwJSONOr returns def when s is blank, so json_valid() CHECK constraints hold.
func fwJSONOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// fwTimeArg renders a nullable time as a NULL-or-UTC insert argument.
func fwTimeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

// fwPtrTime converts a scanned sql.NullTime to a *time.Time.
func fwPtrTime(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	u := nt.Time.UTC()
	return &u
}

// ---- Episodes ---------------------------------------------------------------

// CreateFlywheelEpisode inserts one completed-run record, minting an id if unset.
func (s *Store) CreateFlywheelEpisode(ctx context.Context, ep domain.FlywheelEpisode) (domain.FlywheelEpisode, error) {
	if ep.ID == "" {
		ep.ID = newFlywheelID("ep")
	}
	if ep.CreatedAt.IsZero() {
		ep.CreatedAt = time.Now().UTC()
	}
	ep.CreatedAt = ep.CreatedAt.UTC()
	ep.InputJSON = fwJSONOr(ep.InputJSON, "{}")
	ep.RetrievedJSON = fwJSONOr(ep.RetrievedJSON, "[]")
	ep.TraceJSON = fwJSONOr(ep.TraceJSON, "[]")
	ep.OutcomeJSON = fwJSONOr(ep.OutcomeJSON, "{}")
	ep.FeedbackJSON = fwJSONOr(ep.FeedbackJSON, "{}")

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO fw_episodes (`+fwEpisodeCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ep.ID, ep.ProjectID, ep.SessionID, ep.TaskType, ep.InputJSON, ep.RetrievedJSON,
		ep.TraceJSON, ep.OutcomeJSON, ep.FeedbackJSON, fwTimeArg(ep.ReflectedAt), ep.CreatedAt,
	)
	if err != nil {
		return domain.FlywheelEpisode{}, fmt.Errorf("insert flywheel episode: %w", err)
	}
	return ep, nil
}

func scanFlywheelEpisode(sc rowScanner) (domain.FlywheelEpisode, error) {
	var ep domain.FlywheelEpisode
	var reflectedAt sql.NullTime
	if err := sc.Scan(&ep.ID, &ep.ProjectID, &ep.SessionID, &ep.TaskType, &ep.InputJSON,
		&ep.RetrievedJSON, &ep.TraceJSON, &ep.OutcomeJSON, &ep.FeedbackJSON, &reflectedAt,
		&ep.CreatedAt); err != nil {
		return domain.FlywheelEpisode{}, err
	}
	ep.CreatedAt = ep.CreatedAt.UTC()
	ep.ReflectedAt = fwPtrTime(reflectedAt)
	return ep, nil
}

// GetFlywheelEpisode returns one episode; ok is false when it does not exist.
func (s *Store) GetFlywheelEpisode(ctx context.Context, id string) (domain.FlywheelEpisode, bool, error) {
	row := s.readDB.QueryRowContext(ctx, `SELECT `+fwEpisodeCols+` FROM fw_episodes WHERE id = ?`, id)
	ep, err := scanFlywheelEpisode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.FlywheelEpisode{}, false, nil
	}
	if err != nil {
		return domain.FlywheelEpisode{}, false, fmt.Errorf("get flywheel episode: %w", err)
	}
	return ep, true, nil
}

// ListFlywheelEpisodes returns the most recent episodes for a project.
func (s *Store) ListFlywheelEpisodes(ctx context.Context, projectID string, limit int) ([]domain.FlywheelEpisode, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+fwEpisodeCols+` FROM fw_episodes WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list flywheel episodes: %w", err)
	}
	return collectFlywheelEpisodes(rows)
}

// ListUnreflectedFlywheelEpisodes returns oldest-first episodes not yet consolidated.
func (s *Store) ListUnreflectedFlywheelEpisodes(ctx context.Context, projectID string, limit int) ([]domain.FlywheelEpisode, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+fwEpisodeCols+` FROM fw_episodes WHERE project_id = ? AND reflected_at IS NULL ORDER BY created_at ASC, id ASC LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list unreflected flywheel episodes: %w", err)
	}
	return collectFlywheelEpisodes(rows)
}

func collectFlywheelEpisodes(rows *sql.Rows) ([]domain.FlywheelEpisode, error) {
	defer func() { _ = rows.Close() }()
	out := []domain.FlywheelEpisode{}
	for rows.Next() {
		ep, err := scanFlywheelEpisode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan flywheel episode: %w", err)
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// MarkFlywheelEpisodeReflected stamps an episode as consolidated.
func (s *Store) MarkFlywheelEpisodeReflected(ctx context.Context, id string, reflectedAt time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.writeDB.ExecContext(ctx,
		`UPDATE fw_episodes SET reflected_at = ? WHERE id = ?`, reflectedAt.UTC(), id); err != nil {
		return fmt.Errorf("mark flywheel episode reflected: %w", err)
	}
	return nil
}

// ---- Memory -----------------------------------------------------------------

// CreateFlywheelMemory inserts one long-term memory entry, minting an id if unset.
func (s *Store) CreateFlywheelMemory(ctx context.Context, m domain.FlywheelMemoryEntry) (domain.FlywheelMemoryEntry, error) {
	if !m.Kind.Valid() {
		return domain.FlywheelMemoryEntry{}, fmt.Errorf("invalid flywheel memory kind %q", m.Kind)
	}
	if !m.Status.Valid() {
		return domain.FlywheelMemoryEntry{}, fmt.Errorf("invalid flywheel memory status %q", m.Status)
	}
	if m.ID == "" {
		m.ID = newFlywheelID("mem")
	}
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = now
	}
	m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	if m.Version == 0 {
		m.Version = 1
	}
	m.StructuredJSON = fwJSONOr(m.StructuredJSON, "{}")
	m.ProvenanceJSON = fwJSONOr(m.ProvenanceJSON, "[]")
	m.EvalRefsJSON = fwJSONOr(m.EvalRefsJSON, "[]")

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO fw_memory_entries (`+fwMemoryCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ProjectID, m.Kind, m.ScopeTask, m.ScopeTool, m.Title, m.Body, m.StructuredJSON,
		m.Confidence, m.SupportCount, m.ContradictionCount, m.Status, m.Version, m.ProvenanceJSON,
		m.EvalRefsJSON, m.QuarantineReason, m.CreatedAt, m.UpdatedAt,
		fwTimeArg(m.LastUsedAt), fwTimeArg(m.LastConfirmedAt),
	)
	if err != nil {
		return domain.FlywheelMemoryEntry{}, fmt.Errorf("insert flywheel memory: %w", err)
	}
	return m, nil
}

func scanFlywheelMemory(sc rowScanner) (domain.FlywheelMemoryEntry, error) {
	var m domain.FlywheelMemoryEntry
	var lastUsed, lastConfirmed sql.NullTime
	if err := sc.Scan(&m.ID, &m.ProjectID, &m.Kind, &m.ScopeTask, &m.ScopeTool, &m.Title, &m.Body,
		&m.StructuredJSON, &m.Confidence, &m.SupportCount, &m.ContradictionCount, &m.Status,
		&m.Version, &m.ProvenanceJSON, &m.EvalRefsJSON, &m.QuarantineReason, &m.CreatedAt,
		&m.UpdatedAt, &lastUsed, &lastConfirmed); err != nil {
		return domain.FlywheelMemoryEntry{}, err
	}
	m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	m.LastUsedAt = fwPtrTime(lastUsed)
	m.LastConfirmedAt = fwPtrTime(lastConfirmed)
	return m, nil
}

// GetFlywheelMemory returns one memory entry; ok is false when it does not exist.
func (s *Store) GetFlywheelMemory(ctx context.Context, id string) (domain.FlywheelMemoryEntry, bool, error) {
	row := s.readDB.QueryRowContext(ctx, `SELECT `+fwMemoryCols+` FROM fw_memory_entries WHERE id = ?`, id)
	m, err := scanFlywheelMemory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.FlywheelMemoryEntry{}, false, nil
	}
	if err != nil {
		return domain.FlywheelMemoryEntry{}, false, fmt.Errorf("get flywheel memory: %w", err)
	}
	return m, true, nil
}

// ListFlywheelMemory returns all memory entries for a project, newest first.
func (s *Store) ListFlywheelMemory(ctx context.Context, projectID string) ([]domain.FlywheelMemoryEntry, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+fwMemoryCols+` FROM fw_memory_entries WHERE project_id = ? ORDER BY updated_at DESC, id DESC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list flywheel memory: %w", err)
	}
	return collectFlywheelMemory(rows)
}

// ListFlywheelMemoryByStatus returns entries in one status, best (confidence/support) first.
func (s *Store) ListFlywheelMemoryByStatus(ctx context.Context, projectID string, status domain.MemoryStatus) ([]domain.FlywheelMemoryEntry, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+fwMemoryCols+` FROM fw_memory_entries WHERE project_id = ? AND status = ? ORDER BY confidence DESC, support_count DESC, updated_at DESC, id DESC`,
		projectID, status)
	if err != nil {
		return nil, fmt.Errorf("list flywheel memory by status: %w", err)
	}
	return collectFlywheelMemory(rows)
}

func collectFlywheelMemory(rows *sql.Rows) ([]domain.FlywheelMemoryEntry, error) {
	defer func() { _ = rows.Close() }()
	out := []domain.FlywheelMemoryEntry{}
	for rows.Next() {
		m, err := scanFlywheelMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan flywheel memory: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateFlywheelMemoryStatus transitions a memory entry (e.g. promote, quarantine).
func (s *Store) UpdateFlywheelMemoryStatus(ctx context.Context, id string, status domain.MemoryStatus, quarantineReason string, updatedAt time.Time) error {
	if !status.Valid() {
		return fmt.Errorf("invalid flywheel memory status %q", status)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.writeDB.ExecContext(ctx,
		`UPDATE fw_memory_entries SET status = ?, quarantine_reason = ?, version = version + 1, updated_at = ? WHERE id = ?`,
		status, quarantineReason, updatedAt.UTC(), id); err != nil {
		return fmt.Errorf("update flywheel memory status: %w", err)
	}
	return nil
}

// UpdateFlywheelMemoryStats records consolidation outcomes (confidence + counts).
func (s *Store) UpdateFlywheelMemoryStats(ctx context.Context, id string, confidence float64, supportCount, contradictionCount int, lastConfirmedAt *time.Time, updatedAt time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.writeDB.ExecContext(ctx,
		`UPDATE fw_memory_entries SET confidence = ?, support_count = ?, contradiction_count = ?, last_confirmed_at = ?, updated_at = ? WHERE id = ?`,
		confidence, supportCount, contradictionCount, fwTimeArg(lastConfirmedAt), updatedAt.UTC(), id); err != nil {
		return fmt.Errorf("update flywheel memory stats: %w", err)
	}
	return nil
}

// MarkFlywheelMemoryUsed stamps a memory entry as retrieved/applied.
func (s *Store) MarkFlywheelMemoryUsed(ctx context.Context, id string, lastUsedAt time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.writeDB.ExecContext(ctx,
		`UPDATE fw_memory_entries SET last_used_at = ? WHERE id = ?`, lastUsedAt.UTC(), id); err != nil {
		return fmt.Errorf("mark flywheel memory used: %w", err)
	}
	return nil
}

// ---- Eval cases -------------------------------------------------------------

// CreateFlywheelEvalCase inserts one eval case, minting an id if unset.
func (s *Store) CreateFlywheelEvalCase(ctx context.Context, c domain.FlywheelEvalCase) (domain.FlywheelEvalCase, error) {
	if !c.Kind.Valid() {
		return domain.FlywheelEvalCase{}, fmt.Errorf("invalid flywheel eval kind %q", c.Kind)
	}
	if !c.Polarity.Valid() {
		return domain.FlywheelEvalCase{}, fmt.Errorf("invalid flywheel eval polarity %q", c.Polarity)
	}
	if c.ID == "" {
		c.ID = newFlywheelID("evc")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	c.CreatedAt = c.CreatedAt.UTC()
	c.InputJSON = fwJSONOr(c.InputJSON, "{}")
	c.ReferenceJSON = fwJSONOr(c.ReferenceJSON, "{}")
	c.GradersJSON = fwJSONOr(c.GradersJSON, "[]")

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO fw_eval_cases (`+fwEvalCaseCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ProjectID, c.TaskType, c.Kind, c.Polarity, c.InputJSON, c.ReferenceJSON,
		c.GradersJSON, c.FixtureRef, c.SourceEpisodeID, c.CreatedAt,
	)
	if err != nil {
		return domain.FlywheelEvalCase{}, fmt.Errorf("insert flywheel eval case: %w", err)
	}
	return c, nil
}

func scanFlywheelEvalCase(sc rowScanner) (domain.FlywheelEvalCase, error) {
	var c domain.FlywheelEvalCase
	if err := sc.Scan(&c.ID, &c.ProjectID, &c.TaskType, &c.Kind, &c.Polarity, &c.InputJSON,
		&c.ReferenceJSON, &c.GradersJSON, &c.FixtureRef, &c.SourceEpisodeID, &c.CreatedAt); err != nil {
		return domain.FlywheelEvalCase{}, err
	}
	c.CreatedAt = c.CreatedAt.UTC()
	return c, nil
}

// ListFlywheelEvalCases returns all eval cases for a project, oldest first.
func (s *Store) ListFlywheelEvalCases(ctx context.Context, projectID string) ([]domain.FlywheelEvalCase, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+fwEvalCaseCols+` FROM fw_eval_cases WHERE project_id = ? ORDER BY created_at ASC, id ASC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list flywheel eval cases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.FlywheelEvalCase{}
	for rows.Next() {
		c, err := scanFlywheelEvalCase(rows)
		if err != nil {
			return nil, fmt.Errorf("scan flywheel eval case: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- Eval runs --------------------------------------------------------------

// CreateFlywheelEvalRun inserts one eval-run scoring, minting an id if unset.
func (s *Store) CreateFlywheelEvalRun(ctx context.Context, r domain.FlywheelEvalRun) (domain.FlywheelEvalRun, error) {
	if r.ID == "" {
		r.ID = newFlywheelID("evr")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.MetricsJSON = fwJSONOr(r.MetricsJSON, "{}")
	r.PerCaseJSON = fwJSONOr(r.PerCaseJSON, "[]")

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO fw_eval_runs (`+fwEvalRunCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ProjectID, r.CycleID, r.MemoryVersion, r.StagedMemoryID, r.Ablation,
		r.MetricsJSON, r.PerCaseJSON, r.CreatedAt,
	)
	if err != nil {
		return domain.FlywheelEvalRun{}, fmt.Errorf("insert flywheel eval run: %w", err)
	}
	return r, nil
}

func scanFlywheelEvalRun(sc rowScanner) (domain.FlywheelEvalRun, error) {
	var r domain.FlywheelEvalRun
	if err := sc.Scan(&r.ID, &r.ProjectID, &r.CycleID, &r.MemoryVersion, &r.StagedMemoryID,
		&r.Ablation, &r.MetricsJSON, &r.PerCaseJSON, &r.CreatedAt); err != nil {
		return domain.FlywheelEvalRun{}, err
	}
	r.CreatedAt = r.CreatedAt.UTC()
	return r, nil
}

// GetFlywheelEvalRun returns one eval run; ok is false when it does not exist.
func (s *Store) GetFlywheelEvalRun(ctx context.Context, id string) (domain.FlywheelEvalRun, bool, error) {
	row := s.readDB.QueryRowContext(ctx, `SELECT `+fwEvalRunCols+` FROM fw_eval_runs WHERE id = ?`, id)
	r, err := scanFlywheelEvalRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.FlywheelEvalRun{}, false, nil
	}
	if err != nil {
		return domain.FlywheelEvalRun{}, false, fmt.Errorf("get flywheel eval run: %w", err)
	}
	return r, true, nil
}

// ListFlywheelEvalRuns returns the most recent eval runs for a project.
func (s *Store) ListFlywheelEvalRuns(ctx context.Context, projectID string, limit int) ([]domain.FlywheelEvalRun, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+fwEvalRunCols+` FROM fw_eval_runs WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list flywheel eval runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.FlywheelEvalRun{}
	for rows.Next() {
		r, err := scanFlywheelEvalRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan flywheel eval run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- Consolidation cycles ---------------------------------------------------

// CreateFlywheelConsolidationCycle inserts one cycle audit record.
func (s *Store) CreateFlywheelConsolidationCycle(ctx context.Context, c domain.FlywheelConsolidationCycle) (domain.FlywheelConsolidationCycle, error) {
	if c.ID == "" {
		c.ID = newFlywheelID("cyc")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	c.CreatedAt = c.CreatedAt.UTC()
	c.NetDeltaJSON = fwJSONOr(c.NetDeltaJSON, "{}")

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO fw_consolidation_cycles (`+fwCycleCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ProjectID, c.EpisodesSeen, c.Candidates, c.Promoted, c.Deprecated, c.Quarantined,
		c.EvalRunID, c.NetDeltaJSON, c.CreatedAt,
	)
	if err != nil {
		return domain.FlywheelConsolidationCycle{}, fmt.Errorf("insert flywheel consolidation cycle: %w", err)
	}
	return c, nil
}

func scanFlywheelCycle(sc rowScanner) (domain.FlywheelConsolidationCycle, error) {
	var c domain.FlywheelConsolidationCycle
	if err := sc.Scan(&c.ID, &c.ProjectID, &c.EpisodesSeen, &c.Candidates, &c.Promoted,
		&c.Deprecated, &c.Quarantined, &c.EvalRunID, &c.NetDeltaJSON, &c.CreatedAt); err != nil {
		return domain.FlywheelConsolidationCycle{}, err
	}
	c.CreatedAt = c.CreatedAt.UTC()
	return c, nil
}

// ListFlywheelConsolidationCycles returns cycles for a project, oldest first
// (the natural order for plotting the dual curve).
func (s *Store) ListFlywheelConsolidationCycles(ctx context.Context, projectID string) ([]domain.FlywheelConsolidationCycle, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+fwCycleCols+` FROM fw_consolidation_cycles WHERE project_id = ? ORDER BY created_at ASC, id ASC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list flywheel consolidation cycles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.FlywheelConsolidationCycle{}
	for rows.Next() {
		c, err := scanFlywheelCycle(rows)
		if err != nil {
			return nil, fmt.Errorf("scan flywheel consolidation cycle: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
