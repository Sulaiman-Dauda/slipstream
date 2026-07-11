// panel-agent is Slipstream's privileged daemon. It listens on a root-owned
// Unix socket and executes typed commands from panel-api: provisioning,
// releases, backups, certificates, cache purges, drift checks.
package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/agent"
	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/version"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	socket := env("SLIPSTREAM_AGENT_SOCKET", "/run/slipstream/agent.sock")
	tokenFile := env("SLIPSTREAM_AGENT_TOKEN_FILE", "/etc/slipstream/agent.token")

	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		logger.Error("cannot read agent token", "file", tokenFile, "err", err)
		os.Exit(1)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if len(token) < 32 {
		logger.Error("agent token too short; regenerate with the installer")
		os.Exit(1)
	}

	a := agent.New(logger)
	srv := rpc.NewServer(token, logger)
	srv.SocketGroup = env("SLIPSTREAM_SOCKET_GROUP", "slipstream")
	a.RegisterAll(srv)

	logger.Info("panel-agent listening", "socket", socket, "version", version.Version)
	if err := srv.ListenAndServe(socket); err != nil {
		logger.Error("agent server failed", "err", err)
		os.Exit(1)
	}
}
