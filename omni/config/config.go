package config

type Developer struct {
	Debug bool `json:"debug" jsonschema:"title=Debug,description=Enable debug logging"`
}

type Features struct {
	Memory bool `json:"memory" jsonschema:"title=Memory,description=Enable the memory subsystem"`
	// AutoSync propagates config changes from any agent to all configured agents; default true
	AutoSync         bool `json:"auto_sync"           jsonschema:"title=Auto Sync,description=Propagate config changes from any agent to all configured agents"`
	RandomAgentNames bool `json:"random_agent_names" jsonschema:"title=Random Agent Names,description=Assign random display names to agents"`
	// AxolinkMCP registers and runs the Axolink MCP service; defaults to true
	AxolinkMCP bool `json:"axolink_mcp" jsonschema:"title=Axolink MCP,description=Register and run the Axolink MCP service; defaults to true"`
}

// OmniConfig is the root configuration for omni.
type OmniConfig struct {
	*Features
	Agent *Settings   `json:"agent,omitempty" jsonschema:"title=Agent Settings,description=Common settings applied to the code agent"`
	Dev   *Developer  `json:"dev,omitempty"   jsonschema:"title=Developer,description=Developer and debug flags"`
	Otel  *OtelConfig `json:"otel,omitempty"  jsonschema:"title=Telemetry,description=OpenTelemetry export settings for omni's own logs and traces"`
	// Theme sets the terminal colour theme. Valid values: dark, dark-dim, light, colorblind.
	// Defaults to "dark". Overridden at runtime by OMNI_THEME env var.
	Theme string `json:"theme,omitempty" jsonschema:"title=Theme,description=Terminal colour theme"`
}
