package enginesync

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/Shaik-Sirajuddin/memory/pkg/sockpath"
)

const defaultDialTimeout = 2 * time.Second

// Client pushes session-sync updates to the processing engine over its unix
// socket. It is safe for concurrent use: every call dials a fresh, short-lived
// connection (the engine handles one payload per connection and closes it).
type Client struct {
	socketPath  string
	dialTimeout time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithSocketPath overrides the engine sync socket path. When unset the client
// resolves it via sockpath.EngineSync() (honouring OMNI_ENGINE_SYNC_SOCKET).
func WithSocketPath(path string) Option {
	return func(c *Client) { c.socketPath = path }
}

// WithDialTimeout overrides the per-call dial timeout (default 2s).
func WithDialTimeout(d time.Duration) Option {
	return func(c *Client) { c.dialTimeout = d }
}

// New returns a Client targeting the engine sync socket. By default the path is
// resolved from sockpath.EngineSync(); pass WithSocketPath to override (e.g. in
// tests). The operator typically constructs this once and reuses it.
func New(opts ...Option) *Client {
	c := &Client{socketPath: sockpath.EngineSync(), dialTimeout: defaultDialTimeout}
	for _, o := range opts {
		o(c)
	}
	return c
}

// SocketPath returns the resolved socket path the client dials.
func (c *Client) SocketPath() string { return c.socketPath }

// send dials the engine, writes one JSON payload, and returns. The ctx deadline
// (if any) bounds both the dial and the write.
func (c *Client) send(ctx context.Context, p Payload) error {
	d := net.Dialer{Timeout: c.dialTimeout}
	conn, err := d.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("enginesync: dial %s: %w", c.socketPath, err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
	}
	if err := json.NewEncoder(conn).Encode(p); err != nil {
		return fmt.Errorf("enginesync: encode %s payload to %s: %w", p.Kind, c.socketPath, err)
	}
	return nil
}

// SyncSession sends a payload to the engine. If Kind is unset it defaults to
// KindSessionSync. A non-nil error means the engine did not receive the update.
func (c *Client) SyncSession(ctx context.Context, p Payload) error {
	if p.Kind == "" {
		p.Kind = KindSessionSync
	}
	return c.send(ctx, p)
}

// SyncUsage is a convenience wrapper for the common case: push session usage for
// a single agent identified by agentID.
func (c *Client) SyncUsage(ctx context.Context, agentID string, usage SessionUsage) error {
	return c.send(ctx, Payload{Kind: KindSessionSync, Session: agentID, SessionUsage: usage})
}

// Resume asks the engine to resume agentID: clear its interrupted flag and
// re-trigger delivery, hydrating the agent from the store if it is not yet in
// the engine's in-memory state. Use this instead of seeding via SyncUsage when
// the intent is to (re)activate an agent's delivery loop.
func (c *Client) Resume(ctx context.Context, agentID string) error {
	return c.send(ctx, Payload{Kind: KindResume, Session: agentID})
}
