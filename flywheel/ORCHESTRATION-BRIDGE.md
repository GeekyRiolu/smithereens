# Flywheel — Option 2: learning over real AO worker sessions

**Goal.** Make Flywheel's live batch execute as **real AO worker sessions** (spawned the
way the orchestrator spawns workers) instead of bare `claude -p` calls. Each task becomes
a genuine worker session that appears in the Kanban, runs the real agent against the mock
third-party tools (MCP) with the learned memory injected, and **feeds its outcome back into
the Flywheel learning loop**. So "AO launched these agents and Flywheel learned from them"
becomes literally true — and the learning rate is visible on the Learning tab as sessions
complete.

**Honest scope.** Still the bounded **support-triage** task (its reflector/grader/eval are
task-specific). Making Flywheel learn from *arbitrary* orchestrator coding tasks needs a
general task/eval/reflection framework — that's a separate, much larger effort and is out
of scope here. This bridge changes the **execution substrate** (bare CLI → real AO
session), not the task.

---

## Design

A new `WorkerSessionExecutor` implementing the existing `flywheel.Executor` interface
(drop-in for `SimTriageExecutor` / `LiveExecutor`). Per task, `Run(ctx, task, memory)`:

1. **Compose the task.** Build a triage instruction that tells the agent to use the
   triage MCP tools, apply any learned context, and **write its decision as JSON to
   `flywheel-decision.json` in its workspace** (a deterministic, parse-free way to collect
   the outcome — no transcript scraping).
2. **Spawn a real worker session** in the project (scratch) via AO's session service —
   harness `claude-code`, the triage instruction as the initial task. This is the same
   path the orchestrator's `DelegateTask` uses, so the session shows up on the Kanban.
3. **Provision the workspace.** Drop into the session's worktree: a `.mcp.json` pointing at
   `ao flywheel-tools` (the mock MCP server) and a `FLYWHEEL.md` carrying the injected
   `ComposeLearnedContext(memory)` — so the spawned claude-code discovers both natively.
4. **Wait for completion.** Poll for `<worktree>/flywheel-decision.json` (primary signal),
   bounded by a timeout, with the session's `activity_state` (idle / waiting_input /
   exited) as a secondary signal.
5. **Collect the outcome.** Read the decision file → routing → `Trajectory`; pull token
   usage / cost from the session's `conversation_activities`/usage where available.
6. **Tidy up.** Kill/archive the session (keep it briefly visible, then clean).
7. Return the `Trajectory`. The existing loop grades it, records the episode, reflects,
   and eval-gates — unchanged.

A new demo entry point (`RunSessionDemo` / `POST …/flywheel/session-demo`, async like the
live demo) runs a small cold batch + warm batch of these **real sessions**, so the Kanban
fills with worker cards while the Learning tab's curve/memory update.

```
Learning tab ──POST session-demo──▶ Flywheel ──spawn──▶ AO worker sessions (Kanban)
                                        ▲                        │ run claude + MCP + memory
                                        │                        │ write flywheel-decision.json
              episodes ◀── collect ─────┴──── poll worktree ◀────┘
              reflect → eval-gate → promote → memory grows → curve moves
```

---

## Integration points (to confirm via research, then wire)

- **Spawn API:** the session service's `Spawn`/`DelegateTask` signature + config (project,
  harness, initial task, mode, scratch). Inject a small `SessionRunner` interface into
  `flywheel.Service` (dependency inversion — avoid import cycles); the concrete impl wraps
  the session service, wired in `daemon.go`.
- **Worktree path + timing:** session metadata `WorkspacePath`; when it is available.
- **File provisioning:** write `.mcp.json` + `FLYWHEEL.md` into the worktree (same channel
  adapters use for hooks / `AGENTS.md`). Confirm claude-code reads worktree `.mcp.json`.
- **Completion:** `activity_state` transitions + the decision-file poll.
- **Kill:** `KillSession` signature.
- **Outcome/usage:** reading a finished session's `conversation_activities`.

---

## Risks / mitigations

- **Agent doesn't write the decision file** (nondeterminism) → strict instruction + a
  timeout that records a failed episode (still a valid learning signal).
- **MCP not discovered in-session** → fall back to embedding the tool data in the task
  prompt (as the current `LiveExecutor` already can).
- **Cost + latency** (real sessions are slow/costly) → tiny batches (2–3 per phase);
  clearly a manual, opt-in button.
- **Session-spawn surface is deep** → reuse AO's infra via the existing service; keep the
  `WorkerSessionExecutor` behind the `Executor` seam so the rest of the loop is untouched.
- **Import boundaries** → `flywheel.Service` depends only on a local `SessionRunner`
  interface; the daemon supplies the concrete session service.

---

## Build order

1. `SessionRunner` interface in `service/flywheel` + a fake for tests.
2. `WorkerSessionExecutor` (compose task, spawn, provision worktree, poll, collect, kill)
   + unit tests against the fake.
3. Concrete `SessionRunner` adapter over the session service; wire in `daemon.go`.
4. `RunSessionDemo` + async wrapper + `POST …/flywheel/session-demo` + apispec/regen.
5. Frontend: a "Run as AO worker sessions" button; Kanban shows the workers, Learning tab
   shows the rate.
6. End-to-end on the isolated dev daemon: watch worker cards + curve/memory update.

Verified incrementally (build/test each slice); live end-to-end costs real agent runs.
