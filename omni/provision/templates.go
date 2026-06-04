package provision

// Template is a named provision file template.
type Template struct {
	Name    string
	Content string
}

const teamTemplate = `team:
  name: ""
  repo_url: ""

agents: []
`

const agentSnippet = `- name: ""
  model:
    provider: claude
    model: claude-sonnet-4-6
`

// GetTemplates returns the default provision file templates.
func GetTemplates() []Template {
	return []Template{
		{Name: "provision.yaml", Content: teamTemplate},
		{Name: "agent.snippet.yaml", Content: agentSnippet},
	}
}
