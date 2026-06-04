package provision

import (
	"fmt"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
)

// Provider is a compile-time-validated agent provider identifier.
type Provider = codeagent.Provider

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
	ProviderGemini Provider = "gemini"
)

var validProviders = map[Provider]struct{}{
	ProviderClaude: {},
	ProviderCodex:  {},
	ProviderGemini: {},
}

// Model holds a provider and model name in the format Provider/model_name.
// Provider is validated at compile time; ModelName is validated at runtime via DiscoverAgents().
type Model struct {
	Provider  Provider `json:"provider" yaml:"provider" jsonschema:"title=Provider,description=Agent provider (claude|codex|gemini)"`
	ModelName string   `json:"model"    yaml:"model"    jsonschema:"title=Model,description=Provider-specific model name; validated at runtime via DiscoverAgents()"`
}

// String returns the canonical "Provider/model_name" representation.
func (m Model) String() string {
	return string(m.Provider) + "/" + m.ModelName
}

// ParseModel parses a "Provider/model_name" string into a Model.
func ParseModel(s string) (Model, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Model{}, fmt.Errorf("provision: invalid model format %q: expected Provider/model_name", s)
	}
	p := Provider(parts[0])
	if _, ok := validProviders[p]; !ok {
		return Model{}, fmt.Errorf("provision: unknown provider %q", parts[0])
	}
	return Model{Provider: p, ModelName: parts[1]}, nil
}

// AgentEntry describes a single agent in a provision layout.
type AgentEntry struct {
	Name  string `json:"name"  yaml:"name"  jsonschema:"title=Name,description=Agent name"`
	Model Model  `json:"model" yaml:"model" jsonschema:"title=Model,description=Provider and model for this agent"`
}
