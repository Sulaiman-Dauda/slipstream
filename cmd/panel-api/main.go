// panel-api is Slipstream's unprivileged control plane: the HTTP API and
// embedded UI. Privileged work is delegated to panel-agent over an
// authenticated Unix socket.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
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
	// The way back in for whoever owns the machine. Dispatched first: ahead of the version
	// flag handler, which would otherwise swallow this subcommand's own --help, and ahead of
	// everything the daemon does, so it runs on a box where the panel is up, down, or has
	// never worked at all.
	if len(os.Args) > 1 && os.Args[1] == "recover-admin" {
		if err := runRecoverAdmin(env("SLIPSTREAM_STATE", "/var/lib/slipstream/state.db"), os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "panel-api recover-admin: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if version.HandleFlag(os.Args[1:], "panel-api", `Slipstream's control plane: the HTTP API and embedded web UI. Started by
systemd as slipstream-api.service; it is not normally run by hand.

Runs unprivileged and delegates privileged work to panel-agent over an
authenticated Unix socket.

Locked out? As root on this machine:
  panel-api recover-admin                       list the accounts
  panel-api recover-admin --email you@example.com
  panel-api recover-admin --email you@example.com --disable-2fa

Environment:
  SLIPSTREAM_LISTEN             listen address (default 127.0.0.1:5252)
  SLIPSTREAM_STATE_DIR          panel database directory (default /var/lib/slipstream)
  SLIPSTREAM_AGENT_SOCKET       agent socket path
  SLIPSTREAM_AGENT_TOKEN_FILE   shared-secret file`) {
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	statePath := env("SLIPSTREAM_STATE", "/var/lib/slipstream/state.db")

	socket := env("SLIPSTREAM_AGENT_SOCKET", "/run/slipstream/agent.sock")
	tokenFile := env("SLIPSTREAM_AGENT_TOKEN_FILE", "/etc/slipstream/agent.token")
	listen := env("SLIPSTREAM_LISTEN", "127.0.0.1:5252")
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
	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	server := &api.Server{
		Store:           store,
		Agent:           agentClient,
		Log:             logger,
		UI:              ui.FS(),
		InsecureCookies: dev,
		DefaultPHP:      env("SLIPSTREAM_PHP_VERSION", "8.4"),
		PanelPort:       panelPort,
		Shutdown:        appCtx.Done(),
	}
	server.Init()
	server.StartScheduler(appCtx)

	// First boot: mint the one-time setup URL.
	if n, err := store.CountUsers(); err == nil && n == 0 {
		token := api.NewSetupToken(store, 24*time.Hour)
		logger.Info("setup pending", "url", "https://<server-ip>/setup/"+token,
			"note", "link valid for 24h; restart panel-api to mint a new one")
	}

	handler := server.Routes()

	// Local plaintext listener for the WordPress connector (loopback only).
	localSrv := &http.Server{
		Addr: localListen, Handler: server.ConnectorRoutes(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	go func() {
		logger.Info("connector listener", "addr", localListen)
		if err := localSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("local listener failed", "err", err)
		}
	}()

	mainSrv := &http.Server{
		Addr: listen, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 10 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	inherited, inheritedErr := systemdListener()
	if inheritedErr != nil {
		logger.Error("systemd listener", "err", inheritedErr)
		os.Exit(1)
	}
	go func() {
		var serveErr error
		logger.Info("panel-api listening", "addr", listen, "version", version.Version, "dev", dev)
		if dev {
			serveErr = mainSrv.ListenAndServe()
		} else if inherited != nil {
			serveErr = mainSrv.ServeTLS(inherited, tlsCert, tlsKey)
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
	stopApp() // End SSE streams and scheduler work before draining HTTP.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mainSrv.Shutdown(shutdownCtx)
	localSrv.Shutdown(shutdownCtx)
}

// systemdListener returns the listener passed by slipstream-api.socket.
// Keeping the socket in PID 1 allows connections to queue across binary
// upgrades, eliminating the brief 502 caused by releasing and rebinding it.
func systemdListener() (net.Listener, error) {
	if os.Getenv("LISTEN_FDS") != "1" || os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, nil
	}
	f := os.NewFile(uintptr(3), "slipstream-api.socket")
	if f == nil {
		return nil, fmt.Errorf("invalid inherited listener fd")
	}
	defer f.Close()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("inherit listener: %w", err)
	}
	return ln, nil
}
