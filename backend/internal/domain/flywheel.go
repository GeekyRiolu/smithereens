package domain

import "time"

// Flywheel is AO's self-improving agent runtime. These are the durable records:
// an episodic substrate (one row per completed run) distilled into four typed
// long-term memories, plus the eval suite and consolidation-cycle audit that
// gate what the agent is allowed to remember. See flywheel/ for the design and
// migration 0127_flywheel.sql for the columns.

// MemoryKind is the type of a long-term memory entry.
type MemoryKind string

const (
	// MemoryKnowledge is contextual logic learned from third-party data
	// (conditioned rules with provenance), e.g. a routing or approval rule.
	MemoryKnowledge MemoryKind = "knowledge"
	// MemoryPlaybook is the reliable procedure for a task type.
	MemoryPlaybook MemoryKind = "playbook"
	// MemoryToolCard is how to use one tool/MCP well (params, error->fix, cost).
	MemoryToolCard MemoryKind = "tool_card"
	// MemoryPreference is a user standard (tone, escalation, do/don't).
	MemoryPreference MemoryKind = "preference"
)

// Valid reports whether k is a recognised memory kind.
func (k MemoryKind) Valid() bool {
	switch k {
	case MemoryKnowledge, MemoryPlaybook, MemoryToolCard, MemoryPreference:
		return true
	default:
		return false
	}
}

// MemoryStatus is the lifecycle state of a memory entry.
type MemoryStatus string

const (
	// MemoryCandidate is a proposed lesson awaiting eval-gated promotion.
	MemoryCandidate MemoryStatus = "candidate"
	// MemoryActive is a promoted lesson that retrieval may inject.
	MemoryActive MemoryStatus = "active"
	// MemoryDeprecated is a decayed/superseded lesson kept for audit.
	MemoryDeprecated MemoryStatus = "deprecated"
	// MemoryQuarantined is a lesson that failed the eval gate or contradicts an
	// active one; kept for audit, never retrieved.
	MemoryQuarantined MemoryStatus = "quarantined"
)

// Valid reports whether s is a recognised memory status.
func (s MemoryStatus) Valid() bool {
	switch s {
	case MemoryCandidate, MemoryActive, MemoryDeprecated, MemoryQuarantined:
		return true
	default:
		return false
	}
}

// EvalKind distinguishes capability (a hill to climb) from regression (must
// stay ~100%) eval cases.
type EvalKind string

const (
	// EvalCapability measures "can the agent do this?" — starts low, climbs.
	EvalCapability EvalKind = "capability"
	// EvalRegression measures "does it still work?" — must hold.
	EvalRegression EvalKind = "regression"
)

// Valid reports whether k is a recognised eval kind.
func (k EvalKind) Valid() bool {
	return k == EvalCapability || k == EvalRegression
}

// EvalPolarity marks whether the graded behaviour should occur (positive) or
// should be withheld (negative). Balanced suites need both.
type EvalPolarity string

const (
	// EvalPositive is a case where the behaviour under test should occur.
	EvalPositive EvalPolarity = "positive"
	// EvalNegative is a case where the behaviour under test should NOT occur.
	EvalNegative EvalPolarity = "negative"
)

// Valid reports whether p is a recognised eval polarity.
func (p EvalPolarity) Valid() bool {
	return p == EvalPositive || p == EvalNegative
}

// FlywheelEpisode is one completed run: the raw experience the slow loop learns
// from. The *JSON fields hold structured payloads the service layer interprets
// (kept as raw JSON at the storage boundary).
type FlywheelEpisode struct {
	ID            string
	ProjectID     string
	SessionID     string
	TaskType      string
	InputJSON     string // task + context snapshot (redacted)
	RetrievedJSON string // memory ids injected into this run
	TraceJSON     string // tool calls: tool, args, result, ms, tokens, error
	OutcomeJSON   string // grader scores, success, cost, tokens, tool_calls, wall_ms
	FeedbackJSON  string // human corrections, if any
	ReflectedAt   *time.Time
	CreatedAt     time.Time
}

// FlywheelMemoryEntry is one long-term memory record. Confidence, support and
// contradiction counts let bad lessons decay instead of accumulate; provenance
// and eval refs make every applied lesson explainable.
type FlywheelMemoryEntry struct {
	ID                 string
	ProjectID          string
	Kind               MemoryKind
	ScopeTask          string
	ScopeTool          string
	Title              string
	Body               string
	StructuredJSON     string
	Confidence         float64
	SupportCount       int
	ContradictionCount int
	Status             MemoryStatus
	Version            int
	ProvenanceJSON     string // source episode ids
	EvalRefsJSON       string // eval case ids exercising this lesson
	QuarantineReason   string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastUsedAt         *time.Time
	LastConfirmedAt    *time.Time
}

// FlywheelEvalCase is one frozen test in the suite: inputs plus ordered graders.
type FlywheelEvalCase struct {
	ID              string
	ProjectID       string
	TaskType        string
	Kind            EvalKind
	Polarity        EvalPolarity
	InputJSON       string
	ReferenceJSON   string
	GradersJSON     string
	FixtureRef      string
	SourceEpisodeID string
	CreatedAt       time.Time
}

// FlywheelEvalRun is one scoring of the suite (per cycle, per staged candidate,
// and/or an ablation arm). MetricsJSON holds pass@k/pass^k/tokens/cost/latency.
type FlywheelEvalRun struct {
	ID             string
	ProjectID      string
	CycleID        string
	MemoryVersion  string
	StagedMemoryID string
	Ablation       string // "", "memory_on", "memory_off"
	MetricsJSON    string
	PerCaseJSON    string
	CreatedAt      time.Time
}

// FlywheelConsolidationCycle is one slow-loop cycle: the audit record and the
// dual-curve data point (net_delta vs the previous cycle).
type FlywheelConsolidationCycle struct {
	ID           string
	ProjectID    string
	EpisodesSeen int
	Candidates   int
	Promoted     int
	Deprecated   int
	Quarantined  int
	EvalRunID    string
	NetDeltaJSON string
	CreatedAt    time.Time
}
