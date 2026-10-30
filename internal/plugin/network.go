package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"gengar-pro/internal/executor"
)

// NetworkPlugin provides read-only network diagnostic tools.
// All tools use argv-form exec (no shell), so user-supplied hosts/URLs
// cannot inject shell metacharacters.
//
// Platform: assumes a POSIX environment with ping/dig/curl on PATH.
// The argv flags used here (ping -c -W, dig +short, curl --max-time)
// follow Linux semantics. On Windows the same flags do NOT work
// (Windows ping uses -n, has no dig). gengar's target deployment is
// Linux operations work, so this plugin is registered unconditionally;
// running it on a non-POSIX host will surface errors via IsError=true.
//
// Per-tool timeouts (ping -W, curl --max-time) bound each probe's cost;
// overall lifetime由调用方 ctx 控制。
type NetworkPlugin struct{}

func NewNetworkPlugin() *NetworkPlugin { return &NetworkPlugin{} }

func (np *NetworkPlugin) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "ping",
			Description: "Check connectivity to a host by sending ICMP echo packets (via ping -c).\nUse when: verifying a remote host is reachable; isolating network from application issues; quick sanity before a long operation.\nReturns: ping output with round-trip times, or error if unreachable. Prefer over execute_command + ping.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"host":{"type":"string","description":"Hostname or IP to ping"},"count":{"type":"integer","description":"Number of echo requests (default 4)","default":4},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["host"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "dns_lookup",
			Description: "Resolve a hostname to an IP (or other record type) via dig.\nUse when: debugging \"can't resolve\" errors; confirming a CNAME or MX before configuring a service; verifying DNS changes have propagated.\nReturns: short-format resolution output (one record per line). Prefer over execute_command + dig/nslookup.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"host":{"type":"string","description":"Hostname to resolve"},"type":{"type":"string","description":"DNS record type (A, AAAA, MX, CNAME, TXT, NS); default A","default":"A"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["host"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "http_check",
			Description: "Check whether a URL responds, returning only the HTTP status code and total time (via curl).\nUse when: verifying a web service is up; checking a health endpoint; confirming connectivity through a load balancer. Body is NOT returned to keep output small — use execute_command + curl only if you genuinely need the body.\nReturns: \"<status_code> <total_time_seconds>\\n\" (e.g. \"200 0.123\\n\" — note trailing newline).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"Full URL including scheme"},"timeout":{"type":"integer","description":"Max time in seconds (default 5)","default":5},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["url"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
	}
}

func (np *NetworkPlugin) Execute(ctx context.Context, toolName string, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	switch toolName {
	case "ping":
		var p struct {
			Host  string `json:"host"`
			Count int    `json:"count"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		if p.Count <= 0 {
			p.Count = 4
		}
		return np.exec(ctx, exec, "ping", "-c", strconv.Itoa(p.Count), "-W", "2", p.Host)

	case "dns_lookup":
		var p struct {
			Host string `json:"host"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		if p.Type == "" {
			p.Type = "A"
		}
		return np.exec(ctx, exec, "dig", "+short", p.Host, p.Type)

	case "http_check":
		var p struct {
			URL     string `json:"url"`
			Timeout int    `json:"timeout"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		if p.Timeout <= 0 {
			p.Timeout = 5
		}
		return np.exec(ctx, exec, "curl", "-sS", "-o", "/dev/null",
			"-w", "%{http_code} %{time_total}\n",
			"--max-time", strconv.Itoa(p.Timeout),
			p.URL)

	default:
		return ToolResult{}, fmt.Errorf("NetworkPlugin: unknown tool %q", toolName)
	}
}

func (np *NetworkPlugin) exec(ctx context.Context, exec executor.Executor, name string, args ...string) (ToolResult, error) {
	output, err := exec.RunArgv(ctx, name, args...)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v\n%s", err, output), IsError: true}, nil
	}
	return ToolResult{Content: string(output)}, nil
}
