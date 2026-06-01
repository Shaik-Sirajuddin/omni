package hookoperator

import (
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/omni/config"
)

// entryCache is an in-memory TTL cache of hook entries keyed by event name.
// Entries are replaced atomically on every config-change notification.
// The TTL guards against stale data when the watcher is silent (e.g. restart).
type entryCache struct {
	mu      sync.RWMutex
	entries map[string][]config.HookEntry
	expiry  time.Time
	ttl     time.Duration
}

func newEntryCache(ttlSeconds int) *entryCache {
	return &entryCache{
		entries: map[string][]config.HookEntry{},
		ttl:     time.Duration(ttlSeconds) * time.Second,
	}
}

// set replaces all cached entries and resets the TTL clock.
func (c *entryCache) set(entries map[string][]config.HookEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = entries
	c.expiry = time.Now().Add(c.ttl)
}

// get returns the registered hook entries for eventName.
// The watcher keeps entries fresh; no expiry check here.
func (c *entryCache) get(eventName string) []config.HookEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.entries[eventName]
}

// valid reports whether the cache holds unexpired data.
func (c *entryCache) valid() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Now().Before(c.expiry)
}
