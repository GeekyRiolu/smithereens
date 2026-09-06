-- Flywheel persistence queries. See migration 0127_flywheel.sql.

-- Episodes --------------------------------------------------------------------

-- name: InsertFlywheelEpisode :exec
INSERT INTO fw_episodes (
    id, project_id, session_id, task_type, input_json, retrieved_json,
    trace_json, outcome_json, feedback_json, reflected_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetFlywheelEpisode :one
SELECT id, project_id, session_id, task_type, input_json, retrieved_json,
       trace_json, outcome_json, feedback_json, reflected_at, created_at
FROM fw_episodes
WHERE id = ?;

-- name: ListFlywheelEpisodes :many
SELECT id, project_id, session_id, task_type, input_json, retrieved_json,
       trace_json, outcome_json, feedback_json, reflected_at, created_at
FROM fw_episodes
WHERE project_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListUnreflectedFlywheelEpisodes :many
SELECT id, project_id, session_id, task_type, input_json, retrieved_json,
       trace_json, outcome_json, feedback_json, reflected_at, created_at
FROM fw_episodes
WHERE project_id = ? AND reflected_at IS NULL
ORDER BY created_at ASC, id ASC
LIMIT ?;

-- name: MarkFlywheelEpisodeReflected :exec
UPDATE fw_episodes SET reflected_at = ? WHERE id = ?;

-- Memory ----------------------------------------------------------------------

-- name: InsertFlywheelMemory :exec
INSERT INTO fw_memory_entries (
    id, project_id, kind, scope_task, scope_tool, title, body, structured_json,
    confidence, support_count, contradiction_count, status, version,
    provenance_json, eval_refs_json, quarantine_reason,
    created_at, updated_at, last_used_at, last_confirmed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetFlywheelMemory :one
SELECT id, project_id, kind, scope_task, scope_tool, title, body, structured_json,
       confidence, support_count, contradiction_count, status, version,
       provenance_json, eval_refs_json, quarantine_reason,
       created_at, updated_at, last_used_at, last_confirmed_at
FROM fw_memory_entries
WHERE id = ?;

-- name: ListFlywheelMemory :many
SELECT id, project_id, kind, scope_task, scope_tool, title, body, structured_json,
       confidence, support_count, contradiction_count, status, version,
       provenance_json, eval_refs_json, quarantine_reason,
       created_at, updated_at, last_used_at, last_confirmed_at
FROM fw_memory_entries
WHERE project_id = ?
ORDER BY updated_at DESC, id DESC;

-- name: ListFlywheelMemoryByStatus :many
SELECT id, project_id, kind, scope_task, scope_tool, title, body, structured_json,
       confidence, support_count, contradiction_count, status, version,
       provenance_json, eval_refs_json, quarantine_reason,
       created_at, updated_at, last_used_at, last_confirmed_at
FROM fw_memory_entries
WHERE project_id = ? AND status = ?
ORDER BY confidence DESC, support_count DESC, updated_at DESC, id DESC;

-- name: UpdateFlywheelMemoryStatus :exec
UPDATE fw_memory_entries
SET status = ?, quarantine_reason = ?, version = version + 1, updated_at = ?
WHERE id = ?;

-- name: UpdateFlywheelMemoryStats :exec
UPDATE fw_memory_entries
SET confidence = ?, support_count = ?, contradiction_count = ?,
    last_confirmed_at = ?, updated_at = ?
WHERE id = ?;

-- name: MarkFlywheelMemoryUsed :exec
UPDATE fw_memory_entries SET last_used_at = ? WHERE id = ?;

-- Eval cases ------------------------------------------------------------------

-- name: InsertFlywheelEvalCase :exec
INSERT INTO fw_eval_cases (
    id, project_id, task_type, kind, polarity, input_json, reference_json,
    graders_json, fixture_ref, source_episode_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListFlywheelEvalCases :many
SELECT id, project_id, task_type, kind, polarity, input_json, reference_json,
       graders_json, fixture_ref, source_episode_id, created_at
FROM fw_eval_cases
WHERE project_id = ?
ORDER BY created_at ASC, id ASC;

-- Eval runs -------------------------------------------------------------------

-- name: InsertFlywheelEvalRun :exec
INSERT INTO fw_eval_runs (
    id, project_id, cycle_id, memory_version, staged_memory_id, ablation,
    metrics_json, per_case_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetFlywheelEvalRun :one
SELECT id, project_id, cycle_id, memory_version, staged_memory_id, ablation,
       metrics_json, per_case_json, created_at
FROM fw_eval_runs
WHERE id = ?;

-- name: ListFlywheelEvalRuns :many
SELECT id, project_id, cycle_id, memory_version, staged_memory_id, ablation,
       metrics_json, per_case_json, created_at
FROM fw_eval_runs
WHERE project_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- Consolidation cycles --------------------------------------------------------

-- name: InsertFlywheelConsolidationCycle :exec
INSERT INTO fw_consolidation_cycles (
    id, project_id, episodes_seen, candidates, promoted, deprecated, quarantined,
    eval_run_id, net_delta_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListFlywheelConsolidationCycles :many
SELECT id, project_id, episodes_seen, candidates, promoted, deprecated, quarantined,
       eval_run_id, net_delta_json, created_at
FROM fw_consolidation_cycles
WHERE project_id = ?
ORDER BY created_at ASC, id ASC;
