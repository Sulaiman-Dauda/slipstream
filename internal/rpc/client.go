package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Client is a concurrency-safe RPC client. Calls are serialized over one
// connection; the connection is re-established on failure.
type Client struct {
	path  string
	token string

	mu   sync.Mutex
	conn net.Conn
	r    *bufio.Reader
	next uint64
}

// NewClient creates a client for the agent socket at path.
func NewClient(path, token string) *Client {
	return &Client{path: path, token: token}
}

func (c *Client) connectLocked() error {
	if c.conn != nil {
		return nil
	}
	conn, err := net.DialTimeout("unix", c.path, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial agent: %w", err)
	}
	r := bufio.NewReaderSize(conn, 1<<20)
	if err := json.NewEncoder(conn).Encode(handshake{Auth: c.token}); err != nil {
		conn.Close()
		return fmt.Errorf("handshake write: %w", err)
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return fmt.Errorf("handshake read: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil || !resp.OK {
		conn.Close()
		return fmt.Errorf("agent rejected handshake: %s", resp.Error)
	}
	c.conn, c.r = conn, r
	return nil
}

func (c *Client) dropLocked() {
	if c.conn != nil {
		c.conn.Close()
		c.conn, c.r = nil, nil
	}
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked()
	return nil
}

// Call invokes method with params, decoding the result into out (which may
// be nil). Long-running agent operations are expected: there is no per-call
// timeout beyond dial.
func (c *Client) Call(method string, params any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	// One reconnect attempt if the cached connection has gone away.
	for attempt := 0; attempt < 2; attempt++ {
		if err = c.connectLocked(); err != nil {
			return err
		}
		c.next++
		req := Request{ID: c.next, Method: method, Params: raw}
		if err = json.NewEncoder(c.conn).Encode(req); err != nil {
			c.dropLocked()
			continue
		}
		var line []byte
		line, err = c.r.ReadBytes('\n')
		if err != nil {
			c.dropLocked()
			continue
		}
		var resp Response
		if err = json.Unmarshal(line, &resp); err != nil {
			c.dropLocked()
			return fmt.Errorf("malformed response: %w", err)
		}
		if !resp.OK {
			return fmt.Errorf("agent: %s", resp.Error)
		}
		if out != nil && len(resp.Result) > 0 {
			if err = json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("decode result: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("agent unavailable: %w", err)
}
