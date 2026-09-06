// Package flywheeltools is a minimal stdio MCP server exposing a mock
// "support desk" as third-party tools, for the Flywheel live executor. It lets a
// real Claude Code agent look a customer up and analyze historical tickets — the
// contextual data the agent must learn from — without any external service.
//
// Transport: newline-delimited JSON-RPC 2.0 over stdio (the MCP stdio contract).
// Deterministic + dependency-free so live runs are reproducible and free. It is
// launched as the hidden `ao flywheel-tools` subcommand.
package flywheeltools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// customers maps a customer reference to its plan tier (the hidden attribute
// that makes routing tier-dependent).
var customers = map[string]string{
	"c-eagle": "enterprise", "c-eagle-2": "enterprise",
	"c-otter": "smb", "c-otter-2": "smb",
	"c-finch": "free", "c-finch-2": "free",
}

// groundTruth is the true routing the historical data reflects. It is never
// exposed directly; the agent must infer it from search_similar_tickets.
func groundTruth(tier, keyword string) (string, int) {
	switch {
	case keyword == "outage":
		return "Infra", 1
	case keyword == "dup_charge" && tier == "enterprise":
		return "Billing", 1
	case keyword == "dup_charge":
		return "Billing", 2
	case keyword == "refund":
		return "Billing", 2
	case keyword == "howto":
		return "Support", 3
	default:
		return "Support", 3
	}
}

func toolsList() any {
	return map[string]any{
		"tools": []any{
			map[string]any{
				"name":        "lookup_customer",
				"description": "Look up a customer by reference. Returns the customer's plan tier (enterprise, smb, or free), which affects how issues are prioritized.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"customerRef": map[string]any{"type": "string", "description": "The customer reference, e.g. c-eagle."}},
					"required":   []any{"customerRef"},
				},
			},
			map[string]any{
				"name":        "search_similar_tickets",
				"description": "Search resolved historical tickets matching a keyword (and optionally a tier). Returns how each was routed — the raw data you can analyze to decide correct routing.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"keyword": map[string]any{"type": "string", "description": "Issue keyword, e.g. dup_charge, outage, refund, howto."},
						"tier":    map[string]any{"type": "string", "description": "Optional tier filter: enterprise, smb, free."},
					},
					"required": []any{"keyword"},
				},
			},
		},
	}
}

func textResult(s string) any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}}
}

func callTool(name string, args map[string]any) any {
	switch name {
	case "lookup_customer":
		ref, _ := args["customerRef"].(string)
		tier, ok := customers[strings.TrimSpace(ref)]
		if !ok {
			return textResult(`{"error":"unknown customer"}`)
		}
		return textResult(fmt.Sprintf(`{"customerRef":%q,"tier":%q}`, ref, tier))
	case "search_similar_tickets":
		keyword, _ := args["keyword"].(string)
		tierFilter, _ := args["tier"].(string)
		tiers := []string{"enterprise", "smb", "free"}
		if tierFilter != "" {
			tiers = []string{tierFilter}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Resolved tickets for keyword %q:\n", keyword)
		for _, tier := range tiers {
			team, prio := groundTruth(tier, keyword)
			// Emit several individual resolved tickets (raw data to analyze),
			// with one bit of noise so the majority — not a single row — is the signal.
			for i := 0; i < 4; i++ {
				t, p := team, prio
				if i == 3 { // noise row
					t, p = "Support", 3
				}
				fmt.Fprintf(&b, "- [%s/%s] routed to %s/P%d\n", tier, keyword, t, p)
			}
		}
		return textResult(b.String())
	default:
		return textResult(`{"error":"unknown tool"}`)
	}
}

// Serve runs the MCP stdio server loop, reading newline-delimited JSON-RPC from
// r and writing responses to w until r is closed.
func Serve(r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)
	writer := bufio.NewWriter(w)
	defer func() { _ = writer.Flush() }()

	for {
		line, err := reader.ReadString('\n')
		if line = strings.TrimSpace(line); line != "" {
			handleLine(line, writer)
			_ = writer.Flush()
		}
		if err != nil {
			return nil // stream closed → shut down
		}
	}
}

func handleLine(line string, w *bufio.Writer) {
	var req rpcRequest
	if json.Unmarshal([]byte(line), &req) != nil {
		return
	}
	// Notifications (no id) get no response.
	if len(req.ID) == 0 {
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2024-11-05"
		}
		resp.Result = map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ao-flywheel-tools", "version": "0.1.0"},
		}
	case "tools/list":
		resp.Result = toolsList()
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		resp.Result = callTool(p.Name, p.Arguments)
	case "ping":
		resp.Result = map[string]any{}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	if out, err := json.Marshal(resp); err == nil {
		_, _ = w.Write(out)
		_ = w.WriteByte('\n')
	}
}
