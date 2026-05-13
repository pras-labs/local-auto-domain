package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Client connects to the daemon's Unix socket.
type Client struct {
	http *http.Client
}

func NewClient() *Client {
	sockPath := SocketPath()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", sockPath)
		},
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: 5 * time.Second}}
}

// GetState returns all active domain mappings from the daemon.
func (c *Client) GetState() ([]Entry, error) {
	resp, err := c.http.Get("http://local/state")
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable (is it running?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("daemon returned status %s", resp.Status)
	}
	var entries []Entry
	return entries, json.NewDecoder(resp.Body).Decode(&entries)
}

// IsDaemonRunning returns true if the daemon is reachable.
func (c *Client) IsDaemonRunning() bool {
	_, err := c.GetState()
	return err == nil
}
