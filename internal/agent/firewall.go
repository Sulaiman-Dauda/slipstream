package agent

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// FirewallStatus parses `ufw status`.
func (a *Agent) FirewallStatus() (rpc.FirewallStatusResult, error) {
	out, err := a.Runner.Run(context.Background(), "ufw", "status")
	if err != nil {
		// UFW not installed or inactive is not an error for the UI.
		return rpc.FirewallStatusResult{Enabled: false, Rules: []string{"ufw is not available"}}, nil
	}
	res := rpc.FirewallStatusResult{Enabled: strings.Contains(out, "Status: active")}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Status:") || strings.HasPrefix(line, "To ") || strings.HasPrefix(line, "--") {
			continue
		}
		res.Rules = append(res.Rules, line)
	}
	return res, nil
}

// FirewallRule applies an allow/deny/delete rule via UFW. Ports and
// addresses are validated so nothing user-supplied reaches a shell (there
// is no shell — argv only — but validation still bounds the values).
func (a *Agent) FirewallRule(p rpc.FirewallRuleParams) (map[string]string, error) {
	if p.Port < 1 || p.Port > 65535 {
		return nil, fmt.Errorf("invalid port %d", p.Port)
	}
	proto := p.Proto
	if proto != "tcp" && proto != "udp" {
		proto = "tcp"
	}
	if p.From != "" {
		if net.ParseIP(p.From) == nil {
			if _, _, err := net.ParseCIDR(p.From); err != nil {
				return nil, fmt.Errorf("invalid source address %q", p.From)
			}
		}
	}

	ctx := context.Background()
	portProto := fmt.Sprintf("%d/%s", p.Port, proto)
	var args []string
	switch p.Action {
	case "allow", "deny":
		if p.From != "" {
			args = []string{p.Action, "from", p.From, "to", "any", "port", fmt.Sprintf("%d", p.Port), "proto", proto}
		} else {
			args = []string{p.Action, portProto}
		}
	case "delete":
		args = []string{"delete", "allow", portProto}
	default:
		return nil, fmt.Errorf("invalid action %q", p.Action)
	}
	if _, err := a.Runner.Run(ctx, "ufw", args...); err != nil {
		return nil, err
	}
	return map[string]string{"rule": p.Action + " " + portProto}, nil
}
