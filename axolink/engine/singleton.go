package engine

import (
	"sync"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
)

var (
	instance *ProcessingEngine
	once     sync.Once
)

// Init creates the singleton ProcessingEngine. Safe to call once at startup;
// subsequent calls are no-ops — the first instance is always returned.
func Init(msgStore message.MessageStore, opts ...Option) *ProcessingEngine {
	once.Do(func() {
		instance = New(msgStore, opts...)
	})
	return instance
}

// Get returns the singleton engine. Panics if Init has not been called.
func Get() *ProcessingEngine {
	if instance == nil {
		panic("engine: Get called before Init")
	}
	return instance
}
