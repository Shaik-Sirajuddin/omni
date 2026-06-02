package agentpoolclient

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"

	agentpool "github.com/Shaik-Sirajuddin/memory/svc/agentpool"
)

// Client dials the agent pool daemon over a unix socket.
type Client struct {
	socketPath string
}

// New returns a Client that will connect to socketPath on each call.
func New(socketPath string) *Client {
	return &Client{socketPath: socketPath}
}

// Get requests a pre-warmed session for the given provider and workspace.
// The returned PoolEntry is owned by the caller; the pool has dequeued it.
// The caller is responsible for recording the session_id in its own store.
func (c *Client) Get(provider, workspace string) (*agentpool.PoolEntry, error) {
	resp, err := c.call(agentpool.Request{
		Op:        agentpool.OpGet,
		Provider:  provider,
		Workspace: workspace,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("agentpool: get failed: %s", resp.Error)
	}
	return resp.Entry, nil
}

// RegisterConfig tells the daemon the desired pool depth and parallelism for a
// (provider, workspace) pair. Call once at startup per desired provider.
func (c *Client) RegisterConfig(cfg agentpool.ProviderPoolConfig) error {
	resp, err := c.call(agentpool.Request{
		Op:     agentpool.OpRegisterConfig,
		Config: &cfg,
	})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("agentpool: register_config failed: %s", resp.Error)
	}
	return nil
}

func (c *Client) call(req agentpool.Request) (*agentpool.Response, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("agentpool: dial %s: %w", c.socketPath, err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("agentpool: send request: %w", err)
	}

	var resp agentpool.Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return nil, fmt.Errorf("agentpool: decode response: %w", err)
	}
	return &resp, nil
}
