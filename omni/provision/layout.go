package provision

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// TeamEntry describes the team being provisioned.
type TeamEntry struct {
	Name    string `json:"name"     yaml:"name"     jsonschema:"title=Name,description=Team name"`
	RepoURL string `json:"repo_url" yaml:"repo_url" jsonschema:"title=Repo URL,description=Git repository URL for the team workspace"`
}

// ProvisionLayout is the root struct for a provision YAML file.
type ProvisionLayout struct {
	Team   TeamEntry    `json:"team"   yaml:"team"   jsonschema:"title=Team,description=Team metadata"`
	Agents []AgentEntry `json:"agents" yaml:"agents" jsonschema:"title=Agents,description=List of agents to provision"`
}

// Validate checks that the layout is well-formed.
func (l *ProvisionLayout) Validate() error {
	if l.Team.Name == "" {
		return fmt.Errorf("provision: team.name is required")
	}
	seen := make(map[string]struct{}, len(l.Agents))
	for i, a := range l.Agents {
		if a.Name == "" {
			return fmt.Errorf("provision: agents[%d].name is required", i)
		}
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("provision: duplicate agent name %q", a.Name)
		}
		seen[a.Name] = struct{}{}
		if _, ok := validProviders[a.Model.Provider]; !ok {
			return fmt.Errorf("provision: agents[%d] unknown provider %q", i, a.Model.Provider)
		}
		if a.Model.ModelName == "" {
			return fmt.Errorf("provision: agents[%d].model.model is required", i)
		}
	}
	return nil
}

// GetLayoutFromFile reads, parses, and validates a provision YAML file.
func GetLayoutFromFile(path string) (*ProvisionLayout, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("provision: read %s: %w", path, err)
	}
	var layout ProvisionLayout
	if err := yaml.Unmarshal(data, &layout); err != nil {
		return nil, fmt.Errorf("provision: parse %s: %w", path, err)
	}
	if err := layout.Validate(); err != nil {
		return nil, err
	}
	return &layout, nil
}
