package broadcast

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	storebroadcast "github.com/Shaik-Sirajuddin/memory/mcp/store/broadcast"
)

var (
	ErrEmptyServerID     = errors.New("server_id is required")
	ErrEmptyCallbackTool = errors.New("callback_tool_name is required")
	ErrEmptyEndpoint     = errors.New("endpoint is required")
	ErrUnknownCallback   = errors.New("unknown callback_type")
)

type registry struct {
	mu    sync.RWMutex
	store storebroadcast.RegistryStore
	cache map[string]*MCPClientEntry
}

func newRegistry(store storebroadcast.RegistryStore) *registry {
	logger.Debug("broadcast registry initializing")
	return &registry{
		store: store,
		cache: make(map[string]*MCPClientEntry),
	}
}

func (r *registry) register(ctx context.Context, entry MCPClientEntry) error {
	logger.Debug("broadcast registry register validating", "server_id", entry.ServerID, "callback_type", entry.CallbackType)
	if err := validateEntry(entry); err != nil {
		logger.Error("broadcast registry register validation failed", "err", err, "server_id", entry.ServerID)
		return err
	}
	if entry.UpdatedAt == 0 {
		entry.UpdatedAt = time.Now().UnixMilli()
	}
	logger.Debug("broadcast registry upsert starting", "server_id", entry.ServerID, "updated_at", entry.UpdatedAt)
	if err := r.store.Upsert(ctx, entry); err != nil {
		logger.Error("broadcast registry upsert failed", "err", err, "server_id", entry.ServerID)
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	copied := entry
	r.cache[entry.ServerID] = &copied
	logger.Debug("broadcast registry cache updated", "server_id", entry.ServerID, "cache_size", len(r.cache))
	return nil
}

func (r *registry) unregister(ctx context.Context, serverID string) error {
	if serverID == "" {
		logger.Error("broadcast registry unregister rejected", "err", ErrEmptyServerID)
		return ErrEmptyServerID
	}
	logger.Debug("broadcast registry delete starting", "server_id", serverID)
	if err := r.store.Delete(ctx, serverID); err != nil {
		logger.Error("broadcast registry delete failed", "err", err, "server_id", serverID)
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, serverID)
	logger.Debug("broadcast registry cache deleted", "server_id", serverID, "cache_size", len(r.cache))
	return nil
}

func (r *registry) get(ctx context.Context, serverID string) (*MCPClientEntry, error) {
	if serverID == "" {
		logger.Error("broadcast registry get rejected", "err", ErrEmptyServerID)
		return nil, ErrEmptyServerID
	}

	r.mu.RLock()
	cached, ok := r.cache[serverID]
	r.mu.RUnlock()
	if ok {
		logger.Debug("broadcast registry cache hit", "server_id", serverID)
		copied := *cached
		return &copied, nil
	}

	logger.Debug("broadcast registry cache miss", "server_id", serverID)
	entry, err := r.store.Get(ctx, serverID)
	if err != nil {
		logger.Error("broadcast registry store get failed", "err", err, "server_id", serverID)
		return nil, err
	}

	r.mu.Lock()
	r.cache[serverID] = entry
	cacheSize := len(r.cache)
	r.mu.Unlock()

	logger.Debug("broadcast registry loaded from store", "server_id", serverID, "cache_size", cacheSize)
	copied := *entry
	return &copied, nil
}

func validateEntry(entry MCPClientEntry) error {
	logger.Debug("broadcast registry validate entry",
		"server_id", entry.ServerID,
		"agent_id", entry.AgentID,
		"callback_tool", entry.CallbackToolName,
		"callback_type", entry.CallbackType,
		"endpoint", entry.Endpoint,
		"auth_ref_present", entry.AuthenticationRef != "",
	)
	if entry.ServerID == "" {
		return ErrEmptyServerID
	}
	if entry.CallbackToolName == "" {
		return ErrEmptyCallbackTool
	}
	if entry.Endpoint == "" {
		return ErrEmptyEndpoint
	}
	switch entry.CallbackType {
	case CallbackHTTP, CallbackHTTPOverUnix, CallbackAGCLI:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUnknownCallback, entry.CallbackType)
	}
}
