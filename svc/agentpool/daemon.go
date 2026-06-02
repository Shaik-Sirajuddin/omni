package agentpool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
)

// CreateAgentFunc is called by the daemon to spawn a new detached agent session.
// It must return the agentID and sessionID of the created session.
// Implementations should use Interactive=false so the session starts detached.
type CreateAgentFunc func(ctx context.Context, provider, workspace string) (agentID, sessionID string, err error)

// Daemon is the agent pool service. It holds per-(provider,workspace) queues of
// pre-warmed sessions and serves Get/RegisterConfig requests over a unix socket.
//
// Callers are never throttled. The internal CreateAgentFunc is throttled per
// queue key by a semaphore of size ProviderPoolConfig.MaxParallel (default 5).
type Daemon struct {
	create CreateAgentFunc
	log    *slog.Logger

	mu      sync.Mutex
	configs map[string]ProviderPoolConfig
	queues  map[string][]PoolEntry
	sems    map[string]chan struct{} // key → semaphore (size MaxParallel)
}

// NewDaemon creates a pool daemon backed by the given CreateAgentFunc.
func NewDaemon(create CreateAgentFunc, log *slog.Logger) *Daemon {
	return &Daemon{
		create:  create,
		log:     log,
		configs: make(map[string]ProviderPoolConfig),
		queues:  make(map[string][]PoolEntry),
		sems:    make(map[string]chan struct{}),
	}
}

func queueKey(provider, workspace string) string {
	return provider + ":" + workspace
}

// Run starts the daemon and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context, socketPath string) error {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("agentpool: remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("agentpool: listen %s: %w", socketPath, err)
	}
	defer ln.Close()
	d.log.Info("agentpool: listening", "socket", socketPath)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.log.Warn("agentpool: accept error", "err", err)
			continue
		}
		go d.handleConn(ctx, conn)
	}
}

func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = enc.Encode(Response{OK: false, Error: "invalid request: " + err.Error()})
			continue
		}
		resp := d.dispatch(ctx, req)
		_ = enc.Encode(resp)
	}
}

func (d *Daemon) dispatch(ctx context.Context, req Request) Response {
	switch req.Op {
	case OpGet:
		return d.handleGet(ctx, req.Provider, req.Workspace)
	case OpRegisterConfig:
		if req.Config == nil {
			return Response{OK: false, Error: "register_config: missing config"}
		}
		return d.handleRegisterConfig(*req.Config)
	default:
		return Response{OK: false, Error: "unknown op: " + string(req.Op)}
	}
}

func (d *Daemon) handleRegisterConfig(cfg ProviderPoolConfig) Response {
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 5
	}
	key := queueKey(cfg.Provider, cfg.Workspace)
	d.mu.Lock()
	d.configs[key] = cfg
	if _, ok := d.sems[key]; !ok {
		sem := make(chan struct{}, cfg.MaxParallel)
		for i := 0; i < cfg.MaxParallel; i++ {
			sem <- struct{}{}
		}
		d.sems[key] = sem
	}
	d.mu.Unlock()
	d.log.Info("agentpool: config registered", "provider", cfg.Provider, "workspace", cfg.Workspace, "min", cfg.WorkspaceMin, "maxParallel", cfg.MaxParallel)
	return Response{OK: true}
}

func (d *Daemon) handleGet(ctx context.Context, provider, workspace string) Response {
	key := queueKey(provider, workspace)

	d.mu.Lock()
	// Lazy config registration on first Get.
	if _, ok := d.configs[key]; !ok {
		cfg := ProviderPoolConfig{Provider: provider, Workspace: workspace, WorkspaceMin: 1, MaxParallel: 5}
		d.configs[key] = cfg
		sem := make(chan struct{}, cfg.MaxParallel)
		for i := 0; i < cfg.MaxParallel; i++ {
			sem <- struct{}{}
		}
		d.sems[key] = sem
		d.log.Info("agentpool: lazy config on first Get", "provider", provider, "workspace", workspace)
	}

	if len(d.queues[key]) > 0 {
		entry := d.queues[key][0]
		d.queues[key] = d.queues[key][1:]
		cfg := d.configs[key]
		depth := len(d.queues[key])
		d.mu.Unlock()
		d.log.Info("agentpool: dequeued pre-warmed entry", "provider", provider, "workspace", workspace, "session_id", entry.SessionID)
		go d.replenish(ctx, key, cfg, depth)
		return Response{OK: true, Entry: &entry}
	}
	cfg := d.configs[key]
	d.mu.Unlock()

	d.log.Info("agentpool: queue empty, creating on demand", "provider", provider, "workspace", workspace)
	entry, err := d.spawnOne(ctx, cfg)
	if err != nil {
		d.log.Error("agentpool: on-demand create failed", "provider", provider, "workspace", workspace, "err", err)
		return Response{OK: false, Error: err.Error()}
	}
	return Response{OK: true, Entry: entry}
}

// replenish fills the queue back up to workspace_min in the background.
func (d *Daemon) replenish(ctx context.Context, key string, cfg ProviderPoolConfig, currentDepth int) {
	needed := cfg.WorkspaceMin - currentDepth
	for i := 0; i < needed; i++ {
		if ctx.Err() != nil {
			return
		}
		entry, err := d.spawnOne(ctx, cfg)
		if err != nil {
			d.log.Warn("agentpool: replenish failed", "provider", cfg.Provider, "workspace", cfg.Workspace, "err", err)
			return
		}
		d.mu.Lock()
		d.queues[key] = append(d.queues[key], *entry)
		d.mu.Unlock()
		d.log.Info("agentpool: replenished", "provider", cfg.Provider, "workspace", cfg.Workspace, "session_id", entry.SessionID)
	}
}

// spawnOne acquires the per-key semaphore, calls CreateAgentFunc, releases it.
// At most cfg.MaxParallel calls are in flight per key at any time.
func (d *Daemon) spawnOne(ctx context.Context, cfg ProviderPoolConfig) (*PoolEntry, error) {
	key := queueKey(cfg.Provider, cfg.Workspace)
	d.mu.Lock()
	sem := d.sems[key]
	d.mu.Unlock()

	select {
	case <-sem:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { sem <- struct{}{} }()

	agentID, sessionID, err := d.create(ctx, cfg.Provider, cfg.Workspace)
	if err != nil {
		return nil, fmt.Errorf("agentpool: create agent: %w", err)
	}
	return &PoolEntry{
		SessionID: sessionID,
		AgentID:   agentID,
		Provider:  cfg.Provider,
		Workspace: cfg.Workspace,
	}, nil
}
