package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client is a concurrency-safe RPC client. Each Call dials its own
// short-lived connection, so calls run concurrently and one slow or hung
// agent operation can never block the others (a shared long-lived
// connection would serialize everything behind the slowest call). A
// generous deadline is a safety net against a genuinely wedged operation.
type Client struct {
	path    string
	token   string
	timeout time.Duration
}

// NewClient creates a client for the agent socket at path.
func NewClient(path, token string) *Client {
	return &Client{path: path, token: token, timeout: 30 * time.Minute}
}

// Close is a no-op — connections are per-call and closed when the call ends.
func (c *Client) Close() error { return nil }

// Call invokes method with params, decoding the result into out (which may
// be nil). Long-running agent operations (backups, deploys) are expected;
// the deadline only trips on a truly stuck call.
func (c *Client) Call(method string, params any, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	conn, err := net.DialTimeout("unix", c.path, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial agent: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(c.timeout))

	r := bufio.NewReaderSize(conn, 1<<20)

	// Handshake.
	if err := json.NewEncoder(conn).Encode(handshake{Auth: c.token}); err != nil {
		return fmt.Errorf("handshake write: %w", err)
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("handshake read: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil || !resp.OK {
		return fmt.Errorf("agent rejected handshake: %s", resp.Error)
	}

	// Request/response.
	if err := json.NewEncoder(conn).Encode(Request{ID: 1, Method: method, Params: raw}); err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	line, err = r.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	resp = Response{}
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("malformed response: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("agent: %s", resp.Error)
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}
