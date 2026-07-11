package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// managedServices maps friendly names to systemd units. php-fpm resolves to
// the installed version at call time.
func (a *Agent) unitFor(name string) (string, bool) {
	switch name {
	case "nginx":
		return "nginx", true
	case "mariadb":
		return "mariadb", true
	case "redis":
		return "redis-server", true
	case "php-fpm":
		return a.phpFPMUnit(), true
	case "slipstream-api":
		return "slipstream-api", true
	}
	return "", false
}

// phpFPMUnit finds the installed php-fpm unit (php8.5-fpm, php8.4-fpm, …).
func (a *Agent) phpFPMUnit() string {
	out, err := a.Runner.Run(context.Background(), "systemctl", "list-units",
		"--type=service", "--all", "--no-legend", "--plain", "php*-fpm.service")
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.HasSuffix(fields[0], "-fpm.service") {
				return strings.TrimSuffix(fields[0], ".service")
			}
		}
	}
	return "php8.4-fpm"
}

// RestartService restarts one managed service.
func (a *Agent) RestartService(p rpc.ServiceParams) (map[string]string, error) {
	unit, ok := a.unitFor(p.Name)
	if !ok {
		return nil, fmt.Errorf("unknown service %q", p.Name)
	}
	if _, err := a.Runner.Run(context.Background(), "systemctl", "restart", unit); err != nil {
		return nil, err
	}
	return map[string]string{"restarted": unit}, nil
}

// ServiceStatus reports the state of every managed service.
func (a *Agent) ServiceStatus() (rpc.ServiceStatusResult, error) {
	ctx := context.Background()
	names := []string{"nginx", "php-fpm", "mariadb", "redis"}
	var out rpc.ServiceStatusResult
	for _, n := range names {
		unit, _ := a.unitFor(n)
		active, _ := a.Runner.Run(ctx, "systemctl", "is-active", unit)
		enabled, _ := a.Runner.Run(ctx, "systemctl", "is-enabled", unit)
		out.Services = append(out.Services, rpc.ServiceInfo{
			Name:    n,
			Unit:    unit,
			Active:  strings.TrimSpace(active) == "active",
			Enabled: strings.TrimSpace(enabled) == "enabled",
			Detail:  strings.TrimSpace(active),
		})
	}
	return out, nil
}
