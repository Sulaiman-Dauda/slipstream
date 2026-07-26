// slipctl is Slipstream's command-line client. It authenticates against the
// panel API and drives the same operations as the UI, so everything is
// scriptable.
//
// Environment:
//
//	SLIPSTREAM_URL      panel address (default https://127.0.0.1:8443)
//	SLIPSTREAM_EMAIL    admin email
//	SLIPSTREAM_PASSWORD admin password
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/version"
)

func usage() {
	fmt.Fprintln(os.Stderr, `slipctl — Slipstream command line (`+version.Version+`)

Usage:
  slipctl sites list
  slipctl sites create <domain> <type> [flags-json]
  slipctl sites delete <site-id>
  slipctl purge <site-id> [url ...]
  slipctl staging create <site-id>
  slipctl deploy <site-id> <source-dir>
  slipctl safe-push <site-id>
  slipctl rollback <site-id>
  slipctl backup run <site-id>
  slipctl backup verify <backup-id>
  slipctl backup restore <backup-id> <domain> [full|files|database]
  slipctl database import <site-id> <domain> <site-relative.sql>
  slipctl cron run <cron-id>
  slipctl cert issue <site-id>
  slipctl status
  slipctl drift
  slipctl tasks
  slipctl audit
  slipctl settings get
  slipctl settings set <key> <value>

Types: wordpress woocommerce static php laravel proxy
Example flags-json for wordpress:
  '{"admin_email":"you@example.com","admin_user":"you","admin_password":"...","title":"My Site"}'`)
	os.Exit(2)
}

type cli struct {
	base   string
	client *http.Client
}

func (c *cli) do(method, path string, body any) (int, json.RawMessage) {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			fatal(err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func (c *cli) must(method, path string, body any) json.RawMessage {
	code, out := c.do(method, path, body)
	if code >= 300 {
		fmt.Fprintf(os.Stderr, "error (%d): %s\n", code, out)
		os.Exit(1)
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func pretty(raw json.RawMessage) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		fmt.Println(string(raw))
		return
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	args := os.Args[1:]
	// `slipctl --version` must answer without needing credentials or a
	// reachable panel; usage() below requires neither, but -v/--version did
	// not previously print a bare version at all.
	for _, a := range args {
		if a == "-v" || a == "--version" || a == "version" {
			fmt.Println("slipctl " + version.Version)
			return
		}
	}
	if len(args) == 0 {
		usage()
	}

	jar, _ := cookiejar.New(nil)
	base := strings.TrimSuffix(env("SLIPSTREAM_URL", "https://127.0.0.1:5252"), "/")
	parsed, err := url.Parse(base)
	if err != nil {
		fatal(fmt.Errorf("SLIPSTREAM_URL: %w", err))
	}
	insecure := os.Getenv("SLIPSTREAM_INSECURE") == "1" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost" || parsed.Hostname() == "::1"
	c := &cli{
		base: base,
		client: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
			// Bootstrap certificates are permitted only on loopback or when
			// the operator explicitly opts in. Remote connections verify TLS.
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}},
		},
	}

	email, password := os.Getenv("SLIPSTREAM_EMAIL"), os.Getenv("SLIPSTREAM_PASSWORD")
	if email == "" || password == "" {
		fmt.Fprintln(os.Stderr, "set SLIPSTREAM_EMAIL and SLIPSTREAM_PASSWORD")
		os.Exit(1)
	}
	c.must("POST", "/api/login", map[string]string{"email": email, "password": password, "totp": os.Getenv("SLIPSTREAM_TOTP")})

	switch args[0] {
	case "sites":
		if len(args) < 2 {
			usage()
		}
		switch args[1] {
		case "list":
			pretty(c.must("GET", "/api/sites", nil))
		case "create":
			if len(args) < 4 {
				usage()
			}
			payload := map[string]any{"domain": args[2], "type": args[3]}
			if len(args) > 4 {
				var extra map[string]any
				if err := json.Unmarshal([]byte(args[4]), &extra); err != nil {
					fatal(fmt.Errorf("flags-json: %w", err))
				}
				for k, v := range extra {
					payload[k] = v
				}
			}
			pretty(c.must("POST", "/api/sites", payload))
		case "delete":
			if len(args) < 3 {
				usage()
			}
			pretty(c.must("DELETE", "/api/sites/"+args[2], nil))
		default:
			usage()
		}
	case "purge":
		if len(args) < 2 {
			usage()
		}
		body := map[string]any{}
		if len(args) > 2 {
			body["urls"] = args[2:]
		}
		pretty(c.must("POST", "/api/sites/"+args[1]+"/purge", body))
	case "staging":
		if len(args) < 3 || args[1] != "create" {
			usage()
		}
		pretty(c.must("POST", "/api/sites/"+args[2]+"/staging", map[string]any{}))
	case "deploy":
		if len(args) < 3 {
			usage()
		}
		pretty(c.must("POST", "/api/sites/"+args[1]+"/deployments", map[string]string{"source_dir": args[2]}))
	case "safe-push":
		if len(args) < 2 {
			usage()
		}
		pretty(c.must("POST", "/api/sites/"+args[1]+"/safe-push", map[string]any{}))
	case "rollback":
		if len(args) < 2 {
			usage()
		}
		pretty(c.must("POST", "/api/sites/"+args[1]+"/rollback", map[string]any{}))
	case "backup":
		if len(args) < 3 {
			usage()
		}
		switch args[1] {
		case "run":
			pretty(c.must("POST", "/api/sites/"+args[2]+"/backups", map[string]any{}))
		case "verify":
			pretty(c.must("POST", "/api/backups/"+args[2]+"/verify", map[string]any{}))
		case "restore":
			if len(args) < 4 {
				usage()
			}
			mode := "full"
			if len(args) > 4 {
				mode = args[4]
			}
			pretty(c.must("POST", "/api/backups/"+args[2]+"/restore", map[string]string{"confirm": args[3], "mode": mode}))
		default:
			usage()
		}
	case "database":
		if len(args) != 5 || args[1] != "import" {
			usage()
		}
		pretty(c.must("POST", "/api/sites/"+args[2]+"/database/import", map[string]string{"confirm": args[3], "path": args[4]}))
	case "cron":
		if len(args) != 3 || args[1] != "run" {
			usage()
		}
		pretty(c.must("POST", "/api/cron/"+args[2]+"/run", map[string]any{}))
	case "cert":
		if len(args) < 3 || args[1] != "issue" {
			usage()
		}
		pretty(c.must("POST", "/api/sites/"+args[2]+"/certificate", map[string]any{}))
	case "status":
		pretty(c.must("GET", "/api/system/status", nil))
	case "drift":
		pretty(c.must("GET", "/api/system/drift", nil))
	case "tasks":
		pretty(c.must("GET", "/api/tasks", nil))
	case "audit":
		pretty(c.must("GET", "/api/audit", nil))
	case "settings":
		if len(args) < 2 {
			usage()
		}
		switch args[1] {
		case "get":
			pretty(c.must("GET", "/api/settings", nil))
		case "set":
			if len(args) < 4 {
				usage()
			}
			pretty(c.must("PUT", "/api/settings", map[string]string{args[2]: args[3]}))
		default:
			usage()
		}
	default:
		usage()
	}
}
