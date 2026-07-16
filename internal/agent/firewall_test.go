package agent

import (
	"testing"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// The SSH port must never become unreachable through this API — not just
// via an explicit deny, but also by deleting its allow rule, which has the
// same lockout effect under UFW's default deny-incoming policy.
func TestFirewallRuleNeverLocksOutSSH(t *testing.T) {
	a, run := testAgent(t)

	for _, action := range []string{"deny", "delete"} {
		run.calls = nil
		if _, err := a.FirewallRule(rpc.FirewallRuleParams{Action: action, Port: 22, Proto: "tcp"}); err == nil {
			t.Errorf("action=%s port=22 was accepted, should refuse to touch SSH access", action)
		}
		if run.called("ufw", "delete", "allow", "22/tcp") || run.called("ufw", "deny", "22/tcp") {
			t.Errorf("action=%s port=22 reached ufw despite the guard: %v", action, run.calls)
		}
	}

	// A non-SSH port behaves normally for both actions.
	if _, err := a.FirewallRule(rpc.FirewallRuleParams{Action: "deny", Port: 8080, Proto: "tcp"}); err != nil {
		t.Errorf("deny on a non-SSH port should be allowed: %v", err)
	}
	if _, err := a.FirewallRule(rpc.FirewallRuleParams{Action: "delete", Port: 8080, Proto: "tcp"}); err != nil {
		t.Errorf("delete on a non-SSH port should be allowed: %v", err)
	}
	if _, err := a.FirewallRule(rpc.FirewallRuleParams{Action: "allow", Port: 22, Proto: "tcp"}); err != nil {
		t.Errorf("allow on the SSH port must never be blocked: %v", err)
	}
}
