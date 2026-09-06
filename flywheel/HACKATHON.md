# Flywheel — Hackathon Plan

The full design ([README](README.md) · [ARCHITECTURE](ARCHITECTURE.md) ·
[EVALUATION](EVALUATION.md)) is large. This is the **thin slice** that proves the whole
thesis end-to-end and is buildable in a hackathon — plus an honest line between what's
real and what's stubbed, and a demo script whose beats *are* the four judging questions.

---

## North star: the demo answers the four questions

The demo is not "here's an agent." It's **"here is an agent measurably getting better, and
here's the receipt."** Every beat maps to a question:

| Beat | Question it answers |
|---|---|
| Run cold on a batch → baseline metrics + failures | (sets the baseline) |
| Trigger one consolidation cycle → watch lessons get proposed & eval-gated | **Q1: how does it get better** |
| Learning tab: dual curve moves, memory timeline grows, ablation gap opens | **Q2: show it getting better** |
| Point to one Knowledge rule it discovered from Slack/Linear data + the run that first applied it | **Q3: contextual logic from third-party data** |
| Same tab: $/run, tokens, latency falling as success rises; a graduated skill | **Q4: cost + speed** |

---

## MVP scope

**One** task type, **two** MCP apps, **one** consolidation cycle visible live, **one**
dashboard. Depth over breadth.

### In scope

- **Task:** *support-triage* — incoming message → classify → enrich → file/label a Linear
  issue → draft a reply. (Chosen because contextual-logic learning is vivid here.)
- **Apps (MCP):** Slack (or Intercom) + Linear. Both are in the available MCP roster.
- **Fast loop:** real — a Learning Worker runs as an AO session; episodes are projected
  from `conversation_activities` (`mcp_tool`, `usage`, …).
- **Memory:** all four kinds, but the demo leans on **Knowledge** + **Tool Cards** (most
  visible). SQLite under `~/.ao`, typed records, confidence + provenance.
- **Slow loop:** reflection over the episode batch → typed candidates → **eval-gated**
  promotion. Triggerable by a button for the demo (also schedulable).
- **Evals:** ~25 cases (mostly mined from the cold-run episodes + labeled historical data),
  code graders + one LLM-judge, isolated replay with **fixtured** MCP responses, dual-curve
  + ablation.
- **Dashboard:** the three views (dual curve, memory timeline, ablation).

### Out of scope (explicit cut lines)

- More than one task type or more than two apps.
- Full daemon integration with new migrations + `npm run api`/`sqlc` + CI green — the MVP
  runs memory/eval/reflection as a **sidecar service** reading AO's data, so we don't block
  on the API-contract regeneration pipeline. (Productionizing into `service/memory` etc. is
  the post-hackathon path in [ARCHITECTURE.md](ARCHITECTURE.md#new-package-layout).)
- Skill graduation to *fully autonomous irreversible* actions — demo keeps external writes
  behind approval; graduation is *shown as a state*, not used to send real emails live.
- Cross-project memory, local embedding model (MVP: lexical + structured retrieval, which is
  enough at this scale).

---

## Real vs stubbed (honesty table — say this in the demo)

| Piece | MVP status |
|---|---|
| Learning Worker executing real tasks via AO + MCP | **Real** |
| Episodes projected from AO's `conversation_activities` | **Real** |
| Typed memory store (SQLite, confidence, provenance, decay) | **Real** |
| Reflection → typed candidate lessons | **Real** (bounded LLM call) |
| Eval suite + code graders + eval-gated promotion + ablation | **Real** |
| Third-party data in evals | **Fixtured** (recorded once, replayed) — deterministic, safe |
| Consolidation cadence | **Manual trigger** for the demo (scheduling designed, not shown) |
| Dashboard | **Real** but standalone (not yet the in-app AO Learning tab) |
| External writes (send email/close ticket autonomously) | **Gated by approval**, not fired live |

Being explicit here is a strength: it shows judgment about what's a genuine result vs. a
demo affordance.

---

## Build milestones

Each milestone is independently demoable — if time runs out, stop at any line and you still
have a story.

| # | Milestone | Unlocks | Rough effort |
|---|---|---|---|
| M0 | Learning Worker runs the triage task via AO + 2 MCP apps; one run completes | a real agent doing the task | S |
| M1 | Episode collector: project `conversation_activities` → `fw_episodes` (redacted) | the substrate | S |
| M2 | Memory store + retrieval + injection into system prompt (fenced "Learned context") | memory read path | M |
| M3 | Eval harness: ~25 cases, code graders, isolated replay with fixtures, metric vector | the fitness function | M |
| M4 | Reflection → typed candidates → **eval-gated** promotion; write a cycle row | **the loop closes** | M |
| M5 | Dashboard: dual curve + memory timeline + ablation (live via polling/SSE) | **Q2 proof** | M |
| M6 | Polish: one clear discovered Knowledge rule + the run that first applied it; a graduated skill state | **Q3 + Q4 punch** | S |

Critical path: **M0 → M1 → M4 → M5**. M2/M3 make it real; M6 makes it land.

---

## Suggested tech choices (for speed)

- **Sidecar service:** small Go binary (matches the repo) or a script — reads AO's SQLite
  for activities, owns `fw_*` tables in a separate SQLite file under `~/.ao/flywheel/`.
  Keeps us off the daemon's migration/CI critical path during the hackathon.
- **Reflection + LLM-judge:** Claude (Opus for reflection reasoning, Haiku for grading/
  classification) — the model-routing story is also a talking point.
- **Fixtures:** record real MCP responses once into JSON under `~/.ao/flywheel/fixtures/`,
  replay in evals.
- **Dashboard:** lightweight web view (or an Artifact) reading the sidecar's HTTP/JSON;
  charts for the dual curve, a feed for the memory timeline, a bar for the ablation gap.

---

## Demo script (~5 minutes)

1. **(0:00) Frame it.** "This agent triages support messages. Watch it get *measurably*
   better — and I'll show you the receipts." Show the empty Learning tab.
2. **(0:30) Cold run.** Fire a batch of ~40 messages. It works but stumbles: wrong team on a
   billing case, asks to confirm routing, a couple of tool errors. Baseline row appears:
   pass^k 0.42, $0.21/run, 2.9 tool errors.
3. **(1:30) Consolidate — the reflection moment.** Hit *Run cycle*. The memory timeline
   streams candidates live: a **Knowledge** rule *("Enterprise + 'duplicate charge' →
   Billing/P1 + CC finance", from 19 episodes)*, a **Tool Card** *("slack_search: use
   `in:#billing` filters, CONCISE; backoff on ratelimited")*. Each shows provenance.
4. **(2:30) Eval gate.** Show the suite running in isolated worktrees on fixtured data.
   The Knowledge rule **passes** → promoted (with its eval delta). A weaker candidate is
   **quarantined** — "the system refuses to remember things that don't prove out." (Q1)
5. **(3:15) The curve.** The dual curve jumps: pass^k 0.42 → 0.78, $/run 0.21 → 0.08,
   tool errors → 0.7. Do it once more to show cycle 2 climbing further. (Q2)
6. **(4:00) Contextual logic, with a receipt.** Open the promoted Knowledge rule → its 19
   provenance episodes → the *exact later run* where the agent auto-routed an Enterprise
   duplicate-charge to Billing/P1 and CC'd finance **without asking**. "It learned a
   business rule from your data and applied it." (Q3)
7. **(4:30) Cost + speed.** Point at the falling $/run + latency and the one **graduated
   skill** (mastered playbook, now cached + on a cheaper model). "Competence made it
   cheaper, not more expensive." (Q4)
8. **(4:50) Ablation kicker.** Flip memory OFF on a held-out set — success collapses back
   toward baseline. "That gap is the learning. It's causal, not vibes."

---

## Judging-criteria mapping

| Likely criterion | Where Flywheel scores |
|---|---|
| Technical depth / agent engineering | Two-loop design, typed memory, eval-gated promotion, skill graduation, tool-usage learning |
| Genuinely learns over time | Monotonic, eval-gated, plotted; ablation proves causality |
| Uses third-party tools well | MCP wiring, Tool Cards, discovered macros, contextual-logic learning from tool data |
| Cost/speed awareness | Model routing, skill compilation, prompt-cache prefix, offline consolidation, dual curve |
| Fit with AO | Built on AO's telemetry, worktrees, CDC, lifecycle; one missing loop, not a fork |
| Demo clarity | Every beat maps to a question; the receipt (provenance + ablation) is the differentiator |

---

## Risks & de-risking

- **"Improvement" looks like luck.** → The **ablation** and eval-gate make it causal and
  monotonic; lead with those.
- **MCP flakiness on stage.** → Fixtured evals are deterministic; the live cold-run is the
  only thing hitting real apps, and it's read-mostly.
- **Reflection produces junk lessons.** → That's fine to show — the eval gate *quarantining*
  a bad lesson is a feature, not a failure. Rehearse showing one get rejected.
- **Time.** → Critical path M0→M1→M4→M5; M2/M3 depth and M6 polish are additive. Any stop
  line still tells a coherent story.
