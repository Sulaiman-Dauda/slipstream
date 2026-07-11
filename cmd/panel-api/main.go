// panel-api is Slipstream's unprivileged control plane: the HTTP API and
// embedded UI. Privileged work is delegated to panel-agent over an
// authenticated Unix socket.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/slipstream-panel/slipstream/internal/api"
	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
	"github.com/slipstream-panel/slipstream/internal/version"
	"github.com/slipstream-panel/slipstream/ui"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	statePath := env("SLIPSTREAM_STATE", "/var/lib/slipstream/state.db")
	socket := env("SLIPSTREAM_AGENT_SOCKET", "/run/slipstream/agent.sock")
	tokenFile := env("SLIPSTREAM_AGENT_TOKEN_FILE", "/etc/slipstream/agent.token")
	listen := env("SLIPSTREAM_LISTEN", ":5252")
	localListen := env("SLIPSTREAM_LOCAL_LISTEN", "127.0.0.1:9080")
	tlsCert := env("SLIPSTREAM_TLS_CERT", "/etc/slipstream/certs/panel.pem")
	tlsKey := env("SLIPSTREAM_TLS_KEY", "/etc/slipstream/certs/panel.key")
	dev := os.Getenv("SLIPSTREAM_DEV") == "1"

	store, err := state.Open(statePath)
	if err != nil {
		logger.Error("open state", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		logger.Error("cannot read agent token", "file", tokenFile, "err", err)
		os.Exit(1)
	}
	agentClient := rpc.NewClient(socket, strings.TrimSpace(string(tokenBytes)))
	defer agentClient.Close()

	panelPort := 5252
	if p := env("SLIPSTREAM_PANEL_PORT", ""); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			panelPort = n
		}
	}
	server := &api.Server{
		Store:           store,
		Agent:           agentClient,
		Log:             logger,
		UI:              ui.FS(),
		InsecureCookies: dev,
		DefaultPHP:      env("SLIPSTREAM_PHP_VERSION", "8.4"),
		PanelPort:       panelPort,
	}
	server.Init()
	server.StartScheduler(context.Background())

	// First boot: mint the one-time setup URL.
	if n, err := store.CountUsers(); err == nil && n == 0 {
		token := api.NewSetupToken(store, 20*time.Minute)
		logger.Info("setup pending", "url", "https://<server-ip>"+portOf(listen)+"/setup/"+token,
			"note", "link expires in 20 minutes; restart panel-api to mint a new one")
	}

	handler := server.Routes()

	// Local plaintext listener for the WordPress connector (loopback only).
	localSrv := &http.Server{Addr: localListen, Handler: handler}
	go func() {
		logger.Info("connector listener", "addr", localListen)
		if err := localSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("local listener failed", "err", err)
		}
	}()

	mainSrv := &http.Server{Addr: listen, Handler: handler}
	go func() {
		var serveErr error
		logger.Info("panel-api listening", "addr", listen, "version", version.Version, "dev", dev)
		if dev {
			serveErr = mainSrv.ListenAndServe()
		} else {
			serveErr = mainSrv.ListenAndServeTLS(tlsCert, tlsKey)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Error("server failed", "err", serveErr)
			os.Exit(1)
		}
	}()

	// Graceful shutdown: finish in-flight requests before exiting so a
	// restart or self-update never cuts off a running operation mid-write.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mainSrv.Shutdown(shutdownCtx)
	localSrv.Shutdown(shutdownCtx)
}

func portOf(listen string) string {
	if i := strings.LastIndex(listen, ":"); i >= 0 {
		return listen[i:]
	}
	return ""
}
