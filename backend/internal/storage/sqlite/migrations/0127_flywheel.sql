-- +goose Up

-- Flywheel: self-improving agent runtime. Episodic substrate + four typed
-- long-term memories, plus the eval suite and consolidation-cycle audit that
-- gate what the agent is allowed to remember. These tables deliberately do NOT
-- emit change_log CDC events (its event_type is a closed enum); the Flywheel
-- API is polled by the renderer, mirroring agent_install_jobs.

-- One row per completed run (the raw experience the slow loop learns from).
CREATE TABLE fw_episodes (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    session_id     TEXT NOT NULL DEFAULT '',
    task_type      TEXT NOT NULL,
    input_json     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_json)),
    retrieved_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(retrieved_json)),
    trace_json     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(trace_json)),
    outcome_json   TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(outcome_json)),
    feedback_json  TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(feedback_json)),
    reflected_at   TIMESTAMP,
    created_at     TIMESTAMP NOT NULL
);

CREATE INDEX idx_fw_episodes_project ON fw_episodes (project_id, created_at);
CREATE INDEX idx_fw_episodes_unreflected ON fw_episodes (project_id, reflected_at, created_at);

-- Long-term memory: knowledge | playbook | tool_card | preference.
CREATE TABLE fw_memory_entries (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind                TEXT NOT NULL CHECK (kind IN ('knowledge', 'playbook', 'tool_card', 'preference')),
    scope_task          TEXT NOT NULL DEFAULT '',
    scope_tool          TEXT NOT NULL DEFAULT '',
    title               TEXT NOT NULL,
    body                TEXT NOT NULL DEFAULT '',
    structured_json     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(structured_json)),
    confidence          REAL NOT NULL DEFAULT 0.5,
    support_count       INTEGER NOT NULL DEFAULT 1,
    contradiction_count INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL CHECK (status IN ('candidate', 'active', 'deprecated', 'quarantined')),
    version             INTEGER NOT NULL DEFAULT 1,
    provenance_json     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(provenance_json)),
    eval_refs_json      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(eval_refs_json)),
    quarantine_reason   TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMP NOT NULL,
    updated_at          TIMESTAMP NOT NULL,
    last_used_at        TIMESTAMP,
    last_confirmed_at   TIMESTAMP
);

CREATE INDEX idx_fw_memory_project ON fw_memory_entries (project_id, kind, status);

-- The frozen eval suite: capability + regression cases with graders.
CREATE TABLE fw_eval_cases (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    task_type         TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('capability', 'regression')),
    polarity          TEXT NOT NULL CHECK (polarity IN ('positive', 'negative')),
    input_json        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_json)),
    reference_json    TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(reference_json)),
    graders_json      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(graders_json)),
    fixture_ref       TEXT NOT NULL DEFAULT '',
    source_episode_id TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL
);

CREATE INDEX idx_fw_eval_cases_project ON fw_eval_cases (project_id, task_type);

-- One scoring of the suite (per cycle, and/or per staged candidate, and/or an
-- ablation arm). metrics_json holds pass@k/pass^k/tokens/cost/latency/errors.
CREATE TABLE fw_eval_runs (
    id               TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    cycle_id         TEXT NOT NULL DEFAULT '',
    memory_version   TEXT NOT NULL DEFAULT '',
    staged_memory_id TEXT NOT NULL DEFAULT '',
    ablation         TEXT NOT NULL DEFAULT '' CHECK (ablation IN ('', 'memory_on', 'memory_off')),
    metrics_json     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metrics_json)),
    per_case_json    TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(per_case_json)),
    created_at       TIMESTAMP NOT NULL
);

CREATE INDEX idx_fw_eval_runs_project ON fw_eval_runs (project_id, created_at);

-- One consolidation cycle: the audit record and the dual-curve data point.
CREATE TABLE fw_consolidation_cycles (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    episodes_seen  INTEGER NOT NULL DEFAULT 0,
    candidates     INTEGER NOT NULL DEFAULT 0,
    promoted       INTEGER NOT NULL DEFAULT 0,
    deprecated     INTEGER NOT NULL DEFAULT 0,
    quarantined    INTEGER NOT NULL DEFAULT 0,
    eval_run_id    TEXT NOT NULL DEFAULT '',
    net_delta_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(net_delta_json)),
    created_at     TIMESTAMP NOT NULL
);

CREATE INDEX idx_fw_cycles_project ON fw_consolidation_cycles (project_id, created_at);

-- +goose Down
DROP TABLE fw_consolidation_cycles;
DROP TABLE fw_eval_runs;
DROP TABLE fw_eval_cases;
DROP TABLE fw_memory_entries;
DROP TABLE fw_episodes;
