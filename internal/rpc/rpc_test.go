package rpc

import (
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func startTestServer(t *testing.T, token string) (*Server, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	srv := NewServer(token, slog.Default())
	srv.Handle(MethodPing, func(params json.RawMessage) (any, error) {
		return map[string]string{"pong": "ok"}, nil
	})
	srv.Handle("Echo", func(params json.RawMessage) (any, error) {
		var m map[string]string
		if err := json.Unmarshal(params, &m); err != nil {
			return nil, err
		}
		return m, nil
	})
	srv.Handle("Boom", func(params json.RawMessage) (any, error) {
		return nil, errors.New("kaboom")
	})
	go srv.ListenAndServe(sock)
	// Wait for socket to exist by polling a client ping.
	c := NewClient(sock, token)
	defer c.Close()
	for i := 0; i < 100; i++ {
		if err := c.Call(MethodPing, nil, nil); err == nil {
			return srv, sock
		}
	}
	t.Fatal("server did not come up")
	return nil, ""
}

func TestCallRoundTrip(t *testing.T) {
	_, sock := startTestServer(t, "secret")
	c := NewClient(sock, "secret")
	defer c.Close()

	var out map[string]string
	if err := c.Call("Echo", map[string]string{"hello": "world"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out["hello"] != "world" {
		t.Fatalf("unexpected echo: %v", out)
	}
}

func TestHandlerErrorPropagates(t *testing.T) {
	_, sock := startTestServer(t, "secret")
	c := NewClient(sock, "secret")
	defer c.Close()

	err := c.Call("Boom", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected kaboom, got %v", err)
	}
	// connection must survive a handler error
	if err := c.Call(MethodPing, nil, nil); err != nil {
		t.Fatalf("ping after error: %v", err)
	}
}

func TestBadTokenRejected(t *testing.T) {
	_, sock := startTestServer(t, "secret")
	c := NewClient(sock, "wrong")
	defer c.Close()
	if err := c.Call(MethodPing, nil, nil); err == nil {
		t.Fatal("expected auth rejection")
	}
}

func TestUnknownMethod(t *testing.T) {
	_, sock := startTestServer(t, "secret")
	c := NewClient(sock, "secret")
	defer c.Close()
	err := c.Call("Nope", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("expected unknown method error, got %v", err)
	}
}

func TestConcurrentCalls(t *testing.T) {
	_, sock := startTestServer(t, "secret")
	c := NewClient(sock, "secret")
	defer c.Close()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var out map[string]string
			key := strings.Repeat("k", n+1)
			if err := c.Call("Echo", map[string]string{"key": key}, &out); err != nil {
				t.Errorf("call %d: %v", n, err)
				return
			}
			if out["key"] != key {
				t.Errorf("call %d: wrong result", n)
			}
		}(i)
	}
	wg.Wait()
}
