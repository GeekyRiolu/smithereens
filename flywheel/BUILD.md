# Flywheel — Build Plan & Status

Implementation of the [design](README.md), built **into AO itself** (Go daemon + React
renderer), mock tool environment now / real MCP later. This file is the living task
tracker — updated as slices land.

Ground rules (AO): all state under `~/.ao`; loopback only; **additive migrations only**
(then `npm run sqlc`); code-first API (`npm run api`); thin CLI; adapters are leaves.
Toolchain: Go 1.26.5 (via `mise`), Node 26.

## Decisions locked

- **AO-native** build (backend `service/*`, `storage/sqlite`, `httpd`; renderer tab).
- **No `change_log` CDC** for Flywheel (its `event_type` CHECK is a closed enum). Follow
  the `agent_install_jobs` precedent: the renderer **polls** the Flywheel API. CDC/SSE is
  a later enhancement.
- **Plain SQLite column types** (TEXT/REAL/INTEGER + JSON-in-TEXT) so `sqlc.yaml` needs no
  new overrides; the store maps rows ↔ typed `domain` records.
- **Mock tool environment** (seeded Slack/Linear-style tools) behind a clean tool
  interface; real MCP adapter later.
- **Executor is pluggable**: MVP uses a direct Claude tool-use loop; only the retrieved
  memory changes across runs (keeps "it got better" honest). Falls back to a deterministic
  replay mode when no API key is present, so the demo always runs.

## Phases

| Phase | Scope | State |
|---|---|---|
| **A. Persistence + domain** | migration `0127_flywheel.sql`, `queries/flywheel.sql`, `domain/flywheel.go`, `store/flywheel_store.go` + tests | ⏳ in progress |
| **B. Memory service** | `service/memory` — retrieve (ranked+budgeted), record, propose/promote/quarantine, decay; ports + tests | ☐ |
| **C. Learning loop** | mock env + tools, pluggable executor, deterministic reflector (+ optional LLM), eval harness + graders, eval-gated promotion | ☐ |
| **D. API + CLI** | loopback controllers/DTOs, apispec reg (`npm run api`), thin `ao flywheel …` | ☐ |
| **E. Learning tab** | renderer tab: dual curve, memory timeline, ablation (polling) | ☐ |
| **F. Wiring + demo** | daemon wiring; `ao flywheel demo` runs cold → consolidate → warm to populate the dashboard | ☐ |

Critical path to a demo: **A → B → C → E**. D wires it to the UI; F makes it one command.

## Data model (Phase A)

`fw_episodes` · `fw_memory_entries` · `fw_eval_cases` · `fw_eval_runs` ·
`fw_consolidation_cycles`. See `ARCHITECTURE.md` for field semantics; the migration is the
source of truth for columns.

## Verify commands

```bash
npm run sqlc                                   # regenerate gen/ after query/migration edits
go -C backend build ./...                      # (go via mise)
go -C backend test ./internal/storage/...      # store round-trip tests
npm run frontend:typecheck                     # renderer (Phase E)
```
