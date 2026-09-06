package flywheel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A deterministic, reproducible simulation of the whole thesis on a support-
// triage task. It is NOT a real agent — it stands in for one so the learning
// engine (memory, reflection, eval-gated promotion, the dual curve) can be run
// and proven with no external dependencies. In the AO app the same Executor/
// Grader/Reflector seams take a real AO-session executor and an LLM reflector.
//
// The contextual logic the agent must learn lives ONLY in the data (the ground
// truth below); nothing about it is hardcoded into the executor. The executor
// starts naive and only improves when reflection mines the rules from graded
// episodes and the eval gate promotes them.

const triageTaskType = "support_triage"

type triageInput struct {
	Tier    string `json:"tier"`
	Keyword string `json:"keyword"`
	Agent   string `json:"agent,omitempty"`
}

type triageRouting struct {
	Team     string `json:"team"`
	Priority int    `json:"priority"`
}

// triageRule is the structured payload of a learned knowledge memory: "for this
// tier+keyword, route here". Tier empty means the rule applies to any tier.
type triageRule struct {
	Tier     string `json:"tier,omitempty"`
	Keyword  string `json:"keyword"`
	Team     string `json:"team"`
	Priority int    `json:"priority"`
}

// triageToolCard is the structured payload of a learned tool_card: "this keyword
// can be handled efficiently" (fewer tool calls, fewer tokens, no error).
type triageToolCard struct {
	Keyword string `json:"keyword"`
}

// triageGroundTruth is the hidden contextual logic — the correct routing the
// agent must discover from data. Note the tier-dependent case (enterprise
// dup-charge is P1) and the negative twin risk (free dup-charge is only P2).
func triageGroundTruth(in triageInput) triageRouting {
	switch {
	case in.Keyword == "outage":
		return triageRouting{"Infra", 1}
	case in.Keyword == "dup_charge" && in.Tier == "enterprise":
		return triageRouting{"Billing", 1}
	case in.Keyword == "dup_charge":
		return triageRouting{"Billing", 2}
	case in.Keyword == "refund":
		return triageRouting{"Billing", 2}
	case in.Keyword == "howto":
		return triageRouting{"Support", 3}
	default:
		return triageRouting{"Support", 3}
	}
}

// SimTriageExecutor decides routing using ONLY the supplied memory; with no
// applicable knowledge it falls back to a naive default (wrong for most cases).
type SimTriageExecutor struct{}

func (SimTriageExecutor) Run(_ context.Context, task Task, memory []domain.FlywheelMemoryEntry) (Trajectory, error) {
	var in triageInput
	_ = json.Unmarshal([]byte(task.InputJSON), &in)

	routing := triageRouting{"Support", 3} // naive default
	specificity := -1
	hasToolCard := false
	for _, m := range memory {
		switch m.Kind {
		case domain.MemoryKnowledge:
			var r triageRule
			if json.Unmarshal([]byte(m.StructuredJSON), &r) == nil &&
				r.Keyword == in.Keyword && (r.Tier == "" || r.Tier == in.Tier) {
				spec := 0
				if r.Tier != "" {
					spec = 1 // a tier-specific rule wins over a general one
				}
				if spec > specificity {
					routing = triageRouting{r.Team, r.Priority}
					specificity = spec
				}
			}
		case domain.MemoryToolCard:
			var tc triageToolCard
			if json.Unmarshal([]byte(m.StructuredJSON), &tc) == nil && tc.Keyword == in.Keyword {
				hasToolCard = true
			}
		}
	}

	toolCalls, tokens, toolErrors := 3, 6000, 1
	if hasToolCard {
		toolCalls, tokens, toolErrors = 1, 3000, 0
	}
	trace := []map[string]any{
		{"step": "classify", "tier": in.Tier, "keyword": in.Keyword},
		{"step": "route", "team": routing.Team, "priority": routing.Priority, "toolCalls": toolCalls},
	}
	return Trajectory{
		OutcomeJSON: mustJSON(routing),
		TraceJSON:   mustJSON(trace),
		Tokens:      tokens,
		ToolCalls:   toolCalls,
		ToolErrors:  toolErrors,
		LatencyMS:   1500 + toolCalls*1200,
	}, nil
}

// TriageGrader compares the executor's routing to the case's reference routing.
// It never sees the memory the executor used (outcome verification).
type TriageGrader struct{}

func (TriageGrader) Grade(referenceJSON string, traj Trajectory) (bool, error) {
	var ref, got triageRouting
	if err := json.Unmarshal([]byte(referenceJSON), &ref); err != nil {
		return false, fmt.Errorf("parse reference: %w", err)
	}
	if err := json.Unmarshal([]byte(traj.OutcomeJSON), &got); err != nil {
		return false, fmt.Errorf("parse outcome: %w", err)
	}
	return got == ref, nil
}

// TriageReflector mines conditioned routing rules (and efficiency tool_cards)
// from the observed correct outcomes in episode feedback — the contextual logic,
// learned from data, with provenance. It emits a rule only when a pattern is
// perfectly consistent and supported at least MinSupport times, and picks the
// most general form (keyword-only unless routing depends on tier).
type TriageReflector struct{ MinSupport int }

type triageKT struct{ tier, keyword string }

func (r TriageReflector) Reflect(episodes []domain.FlywheelEpisode) ([]domain.FlywheelMemoryEntry, error) {
	ktVotes := map[triageKT]map[triageRouting]int{}
	ktProv := map[triageKT][]string{}
	kwTiers := map[string]map[string]bool{}

	for _, ep := range episodes {
		var in triageInput
		var fb triageRouting
		if json.Unmarshal([]byte(ep.InputJSON), &in) != nil || json.Unmarshal([]byte(ep.FeedbackJSON), &fb) != nil {
			continue
		}
		k := triageKT{in.Tier, in.Keyword}
		if ktVotes[k] == nil {
			ktVotes[k] = map[triageRouting]int{}
		}
		ktVotes[k][fb]++
		ktProv[k] = append(ktProv[k], ep.ID)
		if kwTiers[in.Keyword] == nil {
			kwTiers[in.Keyword] = map[string]bool{}
		}
		kwTiers[in.Keyword][in.Tier] = true
	}

	var cands []domain.FlywheelMemoryEntry
	for _, kw := range sortedKeys(kwTiers) {
		tiers := sortedSet(kwTiers[kw])
		tierRouting := map[string]triageRouting{}
		var prov []string
		distinct := map[triageRouting]bool{}
		totalSupport := 0
		for _, tier := range tiers {
			k := triageKT{tier, kw}
			rt, ok := consistentRouting(ktVotes[k])
			if !ok {
				continue
			}
			tierRouting[tier] = rt
			distinct[rt] = true
			totalSupport += sumVotes(ktVotes[k])
			prov = append(prov, ktProv[k]...)
		}
		if len(distinct) == 0 {
			continue
		}
		if len(distinct) == 1 && totalSupport >= r.MinSupport {
			var rt triageRouting
			for x := range distinct {
				rt = x
			}
			cands = append(cands, knowledgeCandidate("", kw, rt, prov))
			cands = append(cands, toolCardCandidate(kw, prov))
			continue
		}
		// Routing depends on tier: emit a rule per sufficiently-supported tier.
		for _, tier := range tiers {
			k := triageKT{tier, kw}
			if rt, ok := tierRouting[tier]; ok && sumVotes(ktVotes[k]) >= r.MinSupport {
				cands = append(cands, knowledgeCandidate(tier, kw, rt, ktProv[k]))
			}
		}
		cands = append(cands, toolCardCandidate(kw, prov))
	}

	// A deliberately redundant candidate (equals the naive default) so the demo
	// shows the eval gate quarantining a lesson that does not improve anything.
	cands = append(cands, knowledgeCandidate("", "other", triageRouting{"Support", 3}, nil))
	return cands, nil
}

func knowledgeCandidate(tier, keyword string, rt triageRouting, prov []string) domain.FlywheelMemoryEntry {
	title := fmt.Sprintf("Route %s -> %s/P%d", keyword, rt.Team, rt.Priority)
	if tier != "" {
		title = fmt.Sprintf("Route %s %s -> %s/P%d", tier, keyword, rt.Team, rt.Priority)
	}
	return domain.FlywheelMemoryEntry{
		Kind:           domain.MemoryKnowledge,
		ScopeTask:      triageTaskType,
		Title:          title,
		Body:           fmt.Sprintf("Learned from resolved tickets: %s tickets route to %s priority %d.", keyword, rt.Team, rt.Priority),
		StructuredJSON: mustJSON(triageRule{Tier: tier, Keyword: keyword, Team: rt.Team, Priority: rt.Priority}),
		ProvenanceJSON: mustJSON(prov),
		Confidence:     0.6,
		SupportCount:   len(prov),
	}
}

func toolCardCandidate(keyword string, prov []string) domain.FlywheelMemoryEntry {
	return domain.FlywheelMemoryEntry{
		Kind:           domain.MemoryToolCard,
		ScopeTask:      triageTaskType,
		ScopeTool:      "triage_tools",
		Title:          "Efficient handling for " + keyword,
		Body:           "Use the direct lookup and a CONCISE response; skip the broad search that caused retries.",
		StructuredJSON: mustJSON(triageToolCard{Keyword: keyword}),
		ProvenanceJSON: mustJSON(prov),
		Confidence:     0.6,
		SupportCount:   len(prov),
	}
}

func consistentRouting(votes map[triageRouting]int) (triageRouting, bool) {
	if len(votes) != 1 {
		return triageRouting{}, false
	}
	for rt := range votes {
		return rt, true
	}
	return triageRouting{}, false
}

func sumVotes(votes map[triageRouting]int) int {
	n := 0
	for _, c := range votes {
		n += c
	}
	return n
}

func sortedKeys(m map[string]map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- demo wiring ------------------------------------------------------------

// SeedTriageEvalSuite creates the frozen, balanced eval suite (idempotent).
func (s *Service) SeedTriageEvalSuite(ctx context.Context, projectID string) error {
	existing, err := s.store.ListFlywheelEvalCases(ctx, projectID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	suite := []struct {
		tier, keyword string
		polarity      domain.EvalPolarity
	}{
		{"enterprise", "dup_charge", domain.EvalPositive}, // the key contextual rule: Billing/P1
		{"free", "dup_charge", domain.EvalNegative},       // negative twin: must stay Billing/P2
		{"smb", "dup_charge", domain.EvalPositive},
		{"enterprise", "outage", domain.EvalPositive},
		{"free", "outage", domain.EvalPositive},
		{"smb", "outage", domain.EvalPositive},
		{"enterprise", "refund", domain.EvalPositive},
		{"smb", "refund", domain.EvalPositive},
		{"free", "howto", domain.EvalPositive},
		{"smb", "howto", domain.EvalPositive},
		{"free", "other", domain.EvalNegative}, // already handled by the default
		{"enterprise", "other", domain.EvalNegative},
	}
	for _, c := range suite {
		in := triageInput{Tier: c.tier, Keyword: c.keyword}
		if _, err := s.store.CreateFlywheelEvalCase(ctx, domain.FlywheelEvalCase{
			ProjectID:     projectID,
			TaskType:      triageTaskType,
			Kind:          domain.EvalCapability,
			Polarity:      c.polarity,
			InputJSON:     mustJSON(in),
			ReferenceJSON: mustJSON(triageGroundTruth(in)),
			GradersJSON:   `[{"type":"code","check":"routing_matches_reference"}]`,
		}); err != nil {
			return fmt.Errorf("seed eval case %s/%s: %w", c.tier, c.keyword, err)
		}
	}
	return nil
}

type triageCombo struct {
	tier, keyword string
	count         int
}

func triageBatch(combos []triageCombo) []Task {
	var tasks []Task
	for _, c := range combos {
		for i := 0; i < c.count; i++ {
			tasks = append(tasks, Task{
				ID:        fmt.Sprintf("%s-%s-%d", c.tier, c.keyword, i),
				TaskType:  triageTaskType,
				InputJSON: mustJSON(triageInput{Tier: c.tier, Keyword: c.keyword}),
			})
		}
	}
	return tasks
}

func triageFeedback(t Task) string {
	var in triageInput
	_ = json.Unmarshal([]byte(t.InputJSON), &in)
	return mustJSON(triageGroundTruth(in))
}

// RunTriageDemo runs the full loop: a cold baseline, then learning cycles that
// each execute a batch (attributed to an agent) and consolidate. It introduces
// different slices of the domain over cycles so accuracy climbs gradually while
// cost falls — the dual curve. Returns one report per point (cold first).
func (s *Service) RunTriageDemo(ctx context.Context, projectID string) ([]CycleReport, error) {
	exec := SimTriageExecutor{}
	grader := TriageGrader{}
	reflector := TriageReflector{MinSupport: 3}

	if err := s.SeedTriageEvalSuite(ctx, projectID); err != nil {
		return nil, err
	}

	reports := make([]CycleReport, 0, 4)

	// Cold baseline (no memory) as the first curve point.
	cold, err := s.EvaluateSuite(ctx, projectID, nil, exec, grader)
	if err != nil {
		return nil, err
	}
	coldRun, err := s.store.CreateFlywheelEvalRun(ctx, domain.FlywheelEvalRun{
		ProjectID: projectID, Ablation: "memory_on", MetricsJSON: mustJSON(cold),
	})
	if err != nil {
		return nil, err
	}
	coldCycle, err := s.store.CreateFlywheelConsolidationCycle(ctx, domain.FlywheelConsolidationCycle{
		ProjectID: projectID, EvalRunID: coldRun.ID,
		NetDeltaJSON: mustJSON(map[string]float64{"dAccuracy": 0, "dTokens": 0}),
	})
	if err != nil {
		return nil, err
	}
	reports = append(reports, CycleReport{Cycle: coldCycle, MemoryOn: cold, MemoryOff: cold})

	// Learning cycles, attributed to different agents to show agent-agnosticism.
	cycles := []struct {
		agent  string
		combos []triageCombo
	}{
		{"claude-code", []triageCombo{
			{"enterprise", "dup_charge", 3}, {"smb", "dup_charge", 3}, {"free", "dup_charge", 3},
			{"enterprise", "outage", 3}, {"smb", "outage", 3}, {"free", "outage", 3},
		}},
		{"codex", []triageCombo{
			{"enterprise", "refund", 3}, {"smb", "refund", 3},
			{"free", "howto", 3}, {"smb", "howto", 3},
			{"enterprise", "dup_charge", 3}, {"smb", "dup_charge", 3}, // corroborate tier-dependent dup-charge
		}},
		{"claude-code", []triageCombo{
			{"enterprise", "dup_charge", 3}, {"smb", "dup_charge", 3},
			{"enterprise", "outage", 3}, {"smb", "outage", 3},
			{"enterprise", "refund", 3}, {"smb", "refund", 3},
		}},
	}
	for _, c := range cycles {
		if _, err := s.RunBatch(ctx, projectID, c.agent, triageBatch(c.combos), exec, grader, triageFeedback); err != nil {
			return nil, err
		}
		rep, err := s.Consolidate(ctx, projectID, exec, grader, reflector)
		if err != nil {
			return nil, err
		}
		reports = append(reports, rep)
	}
	return reports, nil
}
