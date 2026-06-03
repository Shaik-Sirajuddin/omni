package client

// Entry is a pre-warmed agent pool entry returned by Get.
type Entry struct {
	AgentID   string
	SessionID string
	Provider  string
	Workspace string
}

// Client dequeues pre-warmed sessions from the agent pool service.
type Client struct {
	addr string
}

// New creates a Client that connects to the pool service at addr.
func New(addr string) *Client {
	return &Client{addr: addr}
}

// Get dequeues a pre-warmed entry for the given provider and workspace.
// Returns (nil, nil) when the pool is empty.
func (c *Client) Get(provider, workspace string) (*Entry, error) {
	// TODO: implement HTTP/gRPC dequeue call to pool service.
	return nil, nil
}
