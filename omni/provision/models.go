package provision

import (
	"sync"
	"time"
)

// ModelList is a cached list of model IDs for a provider.
// Model names are runtime-validated via DiscoverAgents().
type ModelList struct {
	Provider Provider
	models   []string
	cachedAt time.Time
	ttl      time.Duration
	mu       sync.Mutex
}

// NewModelList creates a new ModelList with the given cache TTL.
func NewModelList(provider Provider, ttl time.Duration) *ModelList {
	return &ModelList{Provider: provider, ttl: ttl}
}

// Get returns the cached model list or calls fetch to refresh it.
func (m *ModelList) Get(fetch func(Provider) ([]string, error)) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.models != nil && time.Since(m.cachedAt) < m.ttl {
		return m.models, nil
	}
	models, err := fetch(m.Provider)
	if err != nil {
		return nil, err
	}
	m.models = models
	m.cachedAt = time.Now()
	return m.models, nil
}

// Invalidate clears the cache, forcing the next Get to fetch fresh data.
func (m *ModelList) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.models = nil
	m.cachedAt = time.Time{}
}
