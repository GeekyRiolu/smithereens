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
| **A. Persistence + domain** | migration `0127_flywheel.sql`, `queries/flywheel.sql`, `domain/flywheel.go`, `store/flywheel_store.go` + tests | ✅ done |
| **B. Memory service** | `service/flywheel/memory.go` — retrieve (ranked+budgeted), record, propose (merge/corroborate), promote/quarantine, decay; + tests | ✅ done |
| **C. Learning loop** | executor/grader/reflector interfaces, eval harness, **eval-gated promotion**, cycles; simulated triage domain + `RunTriageDemo`; injection composer | ✅ done |
| **D. API + CLI** | loopback controllers/DTOs exposing every record + a `consolidate/demo` trigger; thin `ao flywheel …` | ⏳ next |
| **E. Learning tab** | renderer tab: dual curve, memory timeline, ablation, episode list (polling) | ☐ |
| **F. Wiring + demo** | daemon wiring; executor = live AO session in the app (mock tools as local MCP) | ☐ |

### Proof (Phase C, `go test ./internal/service/flywheel -run TriageDemo -v`)

```
cycle | accuracy | avgTokens | toolErrRate | promoted | quarantined | ablation(on-off)
  0   |  0.333   |   6000    |    1.00     |    0     |     0       |   0.000   (cold)
  1   |  0.833   |   4500    |    0.50     |    6     |     1       |   0.500
  2   |  1.000   |   3500    |    0.17     |    3     |     2       |   0.667
  3   |  1.000   |   3500    |    0.17     |    0     |     1       |   0.667   (corroboration)
```

Accuracy up, cost down, gate promotes good lessons + quarantines useless ones, ablation
gap proves the gain is causal. Deterministic + reproducible; the LLM/AO-session executor
drops in behind the same `Executor` interface.

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
