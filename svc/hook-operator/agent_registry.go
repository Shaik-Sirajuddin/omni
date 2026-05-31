package hookoperator

import (
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	agy "github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy"
	agyhooks "github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy/hooks"
	claude "github.com/Shaik-Sirajuddin/memory/connector/codeagent/claude"
	claudehooks "github.com/Shaik-Sirajuddin/memory/connector/codeagent/claude/hooks"
	codex "github.com/Shaik-Sirajuddin/memory/connector/codeagent/codex"
)

// agentRegistry owns one HookTransformer per provider, initialized at startup.
// Only providers that have an in-process HookTransformer implementation are held here.
// File-based providers (gemini) write hooks directly to disk and are not tracked.
type agentRegistry struct {
	transformers map[codeagent.Provider]codeagent.HookTransformer
	reg          *registrar
}

func newAgentRegistry(reg *registrar) *agentRegistry {
	ar := &agentRegistry{
		transformers: make(map[codeagent.Provider]codeagent.HookTransformer),
		reg:          reg,
	}
	ar.init()
	return ar
}

// init creates and applies default hooks to all supported in-process transformers.
func (ar *agentRegistry) init() {
	providers := map[codeagent.Provider]codeagent.HookTransformer{
		claude.Claude: claudehooks.New(),
		agy.Agy:       agyhooks.New(),
		codex.Codex:   codex.NewHookTransformer(),
	}
	for provider, transformer := range providers {
		if err := ar.reg.apply(transformer); err != nil {
			logger.Error("hook-operator: agent registry: apply failed", "provider", provider, "err", err)
			continue
		}
		logger.Info("hook-operator: agent registry: hooks registered", "provider", provider)
		ar.transformers[provider] = transformer
	}
}

// Verify reports whether all default hook entries are present for provider.
// Returns (false, allNames, nil) when the provider has no transformer.
func (ar *agentRegistry) Verify(provider codeagent.Provider) (bool, []string, error) {
	transformer, ok := ar.transformers[provider]
	if !ok {
		return false, DefaultHookNames(), nil
	}
	ok2, missing := ar.reg.verify(transformer)
	return ok2, missing, nil
}

// Status returns the verification result for every owned provider.
func (ar *agentRegistry) Status() []ProviderHookStatus {
	statuses := make([]ProviderHookStatus, 0, len(ar.transformers))
	for p := range ar.transformers {
		ok, missing, _ := ar.Verify(p)
		statuses = append(statuses, ProviderHookStatus{
			Provider: p,
			OK:       ok,
			Missing:  missing,
		})
	}
	return statuses
}
