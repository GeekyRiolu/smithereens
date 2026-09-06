package flywheel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// LiveExecutor runs each task as a REAL Claude Code agent (the authed `claude`
// CLI in headless print mode), against the mock third-party tools served over
// MCP by `ao flywheel-tools`, with the retrieved memory injected as an appended
// system prompt. Only the injected memory changes across runs, so any
// improvement is the memory's doing. It implements Executor, so it is a drop-in
// for the deterministic SimTriageExecutor.
type LiveExecutor struct {
	ClaudeBin string // resolved `claude` binary
	AOBin     string // this daemon's own binary; launches `ao flywheel-tools`
	Model     string // optional --model (e.g. "sonnet"); empty = CLI default
	Timeout   time.Duration
}

// NewLiveExecutor resolves the claude + ao binaries and returns a live executor.
// ok is false when the claude CLI is not available, so callers can fall back to
// the simulation.
func NewLiveExecutor() (*LiveExecutor, bool) {
	claudeBin := os.Getenv("AO_FLYWHEEL_CLAUDE_BIN")
	if claudeBin == "" {
		if p, err := exec.LookPath("claude"); err == nil {
			claudeBin = p
		}
	}
	if claudeBin == "" {
		return nil, false
	}
	aoBin, err := os.Executable()
	if err != nil || aoBin == "" {
		// Fall back to `ao` on PATH.
		if p, e := exec.LookPath("ao"); e == nil {
			aoBin = p
		} else {
			return nil, false
		}
	}
	timeout := 180 * time.Second
	e := &LiveExecutor{
		ClaudeBin: claudeBin,
		AOBin:     aoBin,
		Model:     os.Getenv("AO_FLYWHEEL_MODEL"),
		Timeout:   timeout,
	}
	return e, true
}

// refForTier / messageForKeyword turn a (tier, keyword) task into a realistic
// prompt: a customer reference the agent must look up, and a natural message.
func refForTier(tier string) string {
	switch tier {
	case "enterprise":
		return "c-eagle"
	case "smb":
		return "c-otter"
	default:
		return "c-finch"
	}
}

func messageForKeyword(keyword string) string {
	switch keyword {
	case "dup_charge":
		return "We've been charged twice for the same invoice this month."
	case "outage":
		return "Your service has been down for our whole team for an hour."
	case "refund":
		return "I'd like a refund for last month's plan."
	case "howto":
		return "How do I export all of my account data?"
	default:
		return "I have a general question about my account."
	}
}

type claudeResult struct {
	Result       string  `json:"result"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	DurationMS   int     `json:"duration_api_ms"`
	Usage        struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	PermissionDenials []struct {
		ToolName string `json:"tool_name"`
	} `json:"permission_denials"`
}

var routingObjectRe = regexp.MustCompile(`\{[^{}]*"team"[^{}]*\}`)

// Run executes one triage task with the real agent and returns its trajectory.
func (e *LiveExecutor) Run(ctx context.Context, task Task, memory []domain.FlywheelMemoryEntry) (Trajectory, error) {
	var in triageInput
	_ = json.Unmarshal([]byte(task.InputJSON), &in)
	ref := refForTier(in.Tier)
	msg := messageForKeyword(in.Keyword)

	dir, err := os.MkdirTemp("", "fw-live-*")
	if err != nil {
		return Trajectory{}, fmt.Errorf("live: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	mcpPath := filepath.Join(dir, "mcp.json")
	mcpJSON := fmt.Sprintf(`{"mcpServers":{"triage":{"command":%q,"args":["flywheel-tools"]}}}`, e.AOBin)
	if err := os.WriteFile(mcpPath, []byte(mcpJSON), 0o600); err != nil {
		return Trajectory{}, fmt.Errorf("live: write mcp config: %w", err)
	}

	args := []string{
		"-p", e.taskPrompt(ref, msg, in.Keyword),
		"--output-format", "json",
		"--mcp-config", mcpPath, "--strict-mcp-config",
		"--permission-mode", "bypassPermissions",
		"--allowedTools", "mcp__triage__lookup_customer", "mcp__triage__search_similar_tickets",
	}
	if block := ComposeLearnedContext(memory); block != "" {
		memPath := filepath.Join(dir, "mem.txt")
		if err := os.WriteFile(memPath, []byte(block), 0o600); err != nil {
			return Trajectory{}, fmt.Errorf("live: write memory: %w", err)
		}
		args = append(args, "--append-system-prompt-file", memPath)
	}
	if e.Model != "" {
		args = append(args, "--model", e.Model)
	}

	runCtx := ctx
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, e.ClaudeBin, args...)
	cmd.Stdin = bytes.NewReader(nil) // headless: no interactive stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Trajectory{}, fmt.Errorf("live: claude run failed: %w (stderr: %s)", err, truncate(stderr.String(), 300))
	}

	var res claudeResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return Trajectory{}, fmt.Errorf("live: parse claude output: %w", err)
	}
	routing, ok := extractRouting(res.Result)
	if !ok {
		return Trajectory{}, fmt.Errorf("live: no routing JSON in agent output: %s", truncate(res.Result, 300))
	}

	trace := []map[string]any{
		{"step": "agent", "model": e.Model, "turns": res.NumTurns, "costUsd": res.TotalCostUSD},
		{"step": "route", "team": routing.Team, "priority": routing.Priority},
	}
	toolCalls := res.NumTurns - 1
	if toolCalls < 0 {
		toolCalls = 0
	}
	totalTokens := res.Usage.InputTokens + res.Usage.OutputTokens +
		res.Usage.CacheReadTokens + res.Usage.CacheCreationTokens
	return Trajectory{
		OutcomeJSON: mustJSON(routing),
		TraceJSON:   mustJSON(trace),
		Tokens:      totalTokens,
		ToolCalls:   toolCalls,
		ToolErrors:  len(res.PermissionDenials),
		LatencyMS:   res.DurationMS,
		CostUSD:     res.TotalCostUSD,
	}, nil
}

func (e *LiveExecutor) taskPrompt(ref, msg, keyword string) string {
	return fmt.Sprintf(
		"You are a support triage assistant. A customer (reference: %s) wrote: %q (issue keyword: %s). "+
			"Decide the correct team (Billing, Infra, or Support) and priority (1=highest, 2, or 3). "+
			"You have MCP tools to look up the customer's plan tier and to search similar resolved tickets — "+
			"use them only if you need to; if you already know the correct routing, answer directly. "+
			"Respond with ONLY a JSON object like {\"team\":\"Billing\",\"priority\":2}.",
		ref, msg, keyword,
	)
}

// extractRouting pulls the routing decision out of the agent's free-form answer.
func extractRouting(result string) (triageRouting, bool) {
	for _, m := range routingObjectRe.FindAllString(result, -1) {
		var r triageRouting
		if json.Unmarshal([]byte(m), &r) == nil && r.Team != "" && r.Priority > 0 {
			return r, true
		}
	}
	return triageRouting{}, false
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
