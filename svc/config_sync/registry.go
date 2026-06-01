package configsync

import (
	"sync"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
)

// HookParserRegistry maps active agent IDs to their hook transformer.
type HookParserRegistry interface {
	Register(agentID string, t codeagent.HookTransformer)
	Get(agentID string) (codeagent.HookTransformer, bool)
	Unregister(agentID string)
	List() map[string]codeagent.HookTransformer
}

type registry struct {
	mu           sync.RWMutex
	transformers map[string]codeagent.HookTransformer
}

// NewRegistry creates an in-memory hook transformer registry.
func NewRegistry() HookParserRegistry {
	return &registry{
		transformers: map[string]codeagent.HookTransformer{},
	}
}

func (r *registry) Register(agentID string, t codeagent.HookTransformer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transformers[agentID] = t
}

func (r *registry) Get(agentID string) (codeagent.HookTransformer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.transformers[agentID]
	return t, ok
}

func (r *registry) Unregister(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.transformers, agentID)
}

func (r *registry) List() map[string]codeagent.HookTransformer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]codeagent.HookTransformer, len(r.transformers))
	for agentID, t := range r.transformers {
		out[agentID] = t
	}
	return out
}
