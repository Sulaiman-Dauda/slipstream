package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/slipstream-panel/slipstream/internal/tune"
)

// mariaRunner simulates a MariaDB that is unavailable for the first `downFor`
// connection attempts after a restart, which is what `systemctl restart`
// actually looks like: the unit is "started" before the server accepts
// connections.
type mariaRunner struct {
	mu           sync.Mutex
	cmds         []string
	restarted    bool
	downFor      int // failing SELECT 1 attempts remaining after a restart
	sqlWhileDown int // SQL statements that reached a server that was still down
}

func (r *mariaRunner) record(name string, args ...string) {
	r.cmds = append(r.cmds, strings.TrimSpace(name+" "+strings.Join(args, " ")))
}

func (r *mariaRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record(name, args...)

	joined := strings.Join(args, " ")
	switch {
	case name == "systemctl" && strings.Contains(joined, "restart mariadb"):
		r.restarted = true
		return "", nil
	case name == "mariadb" && strings.Contains(joined, "SELECT 1"):
		if r.downFor > 0 {
			r.downFor--
			return "", fmt.Errorf("ERROR 2002 (HY000): Can't connect to local server")
		}
		return "1", nil
	case name == "mariadb":
		if r.downFor > 0 {
			r.sqlWhileDown++
			return "", fmt.Errorf("ERROR 2002 (HY000): Can't connect to local server")
		}
		return "", nil
	}
	return "", nil
}

func (r *mariaRunner) RunStdin(_ context.Context, _ string, name string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record(name, args...)
	if name == "mariadb" && r.downFor > 0 {
		r.sqlWhileDown++
		return "", fmt.Errorf("ERROR 2002 (HY000): Can't connect to local server")
	}
	return "", nil
}

func (r *mariaRunner) RunCombined(ctx context.Context, name string, args ...string) (string, error) {
	return r.Run(ctx, name, args...)
}

func quietAgent(r Runner) *Agent {
	return &Agent{Runner: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// The first database creation on a fresh install writes MariaDB tuning and
// restarts the server. Before this was fixed, tuneMariaDBIfNeeded returned as
// soon as `systemctl restart` did — so the CREATE DATABASE that followed hit a
// server that was still starting and the whole provision failed with
// "ERROR 2002 (HY000): Can't connect to local server". Reproduced on a real
// 2-vCPU box by provisioning four sites at once.
func TestTuneMariaDBWaitsForServerToAcceptConnections(t *testing.T) {
	tmp := t.TempDir()
	orig := tune.ConfigPath
	tune.ConfigPath = filepath.Join(tmp, "60-slipstream.cnf")
	t.Cleanup(func() { tune.ConfigPath = orig })

	r := &mariaRunner{downFor: 3}
	a := quietAgent(r)
	a.Paths = DefaultPaths()
	a.Paths.SitesRoot = tmp

	a.tuneMariaDBIfNeeded(context.Background())

	if !r.restarted {
		t.Fatal("expected MariaDB to be restarted after tuning was written")
	}
	if r.downFor != 0 {
		t.Fatalf("returned while MariaDB was still refusing connections (%d attempts left)", r.downFor)
	}
	// The readiness probe must be the last thing it does, so a caller issuing
	// SQL immediately afterwards reaches a server that is actually up.
	last := r.cmds[len(r.cmds)-1]
	if !strings.Contains(last, "SELECT 1") {
		t.Fatalf("last command before returning was %q, want the readiness probe", last)
	}
	if _, err := os.Stat(tune.ConfigPath); err != nil {
		t.Fatalf("tuning config was not written: %v", err)
	}
}

// Concurrent provisioning is the case that actually failed in the wild: one
// site triggers the tuning restart while another is midway through creating its
// own database. Every caller must serialise behind the restart.
func TestTuneMariaDBSerialisesConcurrentCallers(t *testing.T) {
	tmp := t.TempDir()
	orig := tune.ConfigPath
	tune.ConfigPath = filepath.Join(tmp, "60-slipstream.cnf")
	t.Cleanup(func() { tune.ConfigPath = orig })

	r := &mariaRunner{downFor: 4}
	a := quietAgent(r)
	a.Paths = DefaultPaths()
	a.Paths.SitesRoot = tmp

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.tuneMariaDBIfNeeded(context.Background())
			// Stand in for the CREATE DATABASE that follows in DatabaseCreate.
			_, _ = a.Runner.RunStdin(context.Background(), "CREATE DATABASE x;", "mariadb", "--protocol=socket")
		}()
	}
	wg.Wait()

	if r.sqlWhileDown != 0 {
		t.Fatalf("%d SQL statements were issued while MariaDB was down; provisioning would fail", r.sqlWhileDown)
	}
	restarts := 0
	for _, c := range r.cmds {
		if strings.Contains(c, "restart mariadb") {
			restarts++
		}
	}
	if restarts != 1 {
		t.Fatalf("MariaDB restarted %d times, want exactly 1 — tuning must happen once", restarts)
	}
}
