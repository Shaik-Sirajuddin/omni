package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
)

// mcpMu guards all read-modify-write operations on config.toml for MCP entries.
var mcpMu sync.RWMutex

// configPathForMCP returns the config.toml path scoped by global vs workspace.
func (a *codexAgent) configPathForMCP(global bool) (string, error) {
	if global {
		return globalConfigPath()
	}
	return filepath.Join(a.workDir, ".codex", "config.toml"), nil
}

// withMCPConfig runs fn under the file lock and mcpMu, providing the full raw
// TOML map. The file lock (via a.locker) serializes external processes; mcpMu
// serializes goroutines within this process.
func (a *codexAgent) withMCPConfig(path string, fn func(raw map[string]interface{}) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("codex: mkdir %s: %w", filepath.Dir(path), err)
	}
	lockPath := filepath.Join(filepath.Dir(path), ".config.toml.lock")
	unlock, err := a.locker.Lock(lockPath)
	if err != nil {
		return err
	}
	defer unlock()

	mcpMu.Lock()
	defer mcpMu.Unlock()

	raw, err := readConfigTOML(path)
	if err != nil {
		return err
	}
	if err := fn(raw); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return fmt.Errorf("codex: encode config.toml: %w", err)
	}
	return a.locker.WriteAtomic(path, buf.Bytes())
}

// mcpServerToRaw converts MCPServer → map[string]interface{} using JSON as
// intermediate so that snake_case keys from json tags are preserved in TOML.
func mcpServerToRaw(s codeagent.MCPServer) (map[string]interface{}, error) {
	cfg := RawMcpServerConfig{}
	enabled := true
	cfg.Enabled = &enabled

	switch s.Transport {
	case codeagent.MCPTransportStdio:
		if s.Command != "" {
			cfg.Command = &s.Command
		}
		cfg.Args = s.Args
	case codeagent.MCPTransportHTTP, codeagent.MCPTransportSSE:
		if s.URL != "" {
			cfg.Url = &s.URL
		}
	}

	if len(s.Env) > 0 {
		cfg.Env = RawMcpServerConfigEnv(s.Env)
	}
	if len(s.EnvVars) > 0 {
		cfg.EnvVars = s.EnvVars
	}
	if len(s.Headers) > 0 {
		cfg.HttpHeaders = RawMcpServerConfigHttpHeaders(s.Headers)
	}
	if len(s.EnvHeaders) > 0 {
		cfg.EnvHttpHeaders = RawMcpServerConfigEnvHttpHeaders(s.EnvHeaders)
	}
	if s.Timeout > 0 {
		ms := s.Timeout
		cfg.StartupTimeoutMs = &ms
	}

	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// rawToMCPServer converts a raw TOML map entry → MCPServer.
func rawToMCPServer(name string, v interface{}) (codeagent.MCPServer, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return codeagent.MCPServer{}, err
	}
	var cfg RawMcpServerConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return codeagent.MCPServer{}, err
	}

	s := codeagent.MCPServer{Name: name}
	if cfg.Command != nil {
		s.Transport = codeagent.MCPTransportStdio
		s.Command = *cfg.Command
		s.Args = cfg.Args
	} else if cfg.Url != nil {
		s.Transport = codeagent.MCPTransportHTTP
		s.URL = *cfg.Url
	}
	if len(cfg.Env) > 0 {
		s.Env = map[string]string(cfg.Env)
	}
	if len(cfg.HttpHeaders) > 0 {
		s.Headers = map[string]string(cfg.HttpHeaders)
	}
	if len(cfg.EnvHttpHeaders) > 0 {
		s.EnvHeaders = map[string]string(cfg.EnvHttpHeaders)
	}
	if cfg.StartupTimeoutMs != nil {
		s.Timeout = *cfg.StartupTimeoutMs
	}
	return s, nil
}

// getMCPServersRaw returns the raw mcp_servers map from a config map.
func getMCPServersRaw(raw map[string]interface{}) map[string]interface{} {
	m, _ := raw["mcp_servers"].(map[string]interface{})
	if m == nil {
		m = map[string]interface{}{}
	}
	return m
}

// ============================================================
// MCPManager implementation
// ============================================================

func (a *codexAgent) AddMCP(p codeagent.AddMCPParams) (*codeagent.AddMCPResult, error) {
	path, err := a.configPathForMCP(p.Global)
	if err != nil {
		return nil, err
	}
	err = a.withMCPConfig(path, func(raw map[string]interface{}) error {
		servers := getMCPServersRaw(raw)
		entry, err := mcpServerToRaw(p.Server)
		if err != nil {
			return fmt.Errorf("codex: add mcp %q: %w", p.Server.Name, err)
		}
		servers[p.Server.Name] = entry
		raw["mcp_servers"] = servers
		return nil
	})
	if err != nil {
		return nil, err
	}
	logger.Debug("AddMCP", "name", p.Server.Name, "global", p.Global)
	return &codeagent.AddMCPResult{}, nil
}

func (a *codexAgent) ListMCP(p codeagent.ListMCPParams) (*codeagent.ListMCPResult, error) {
	path, err := a.configPathForMCP(p.Global)
	if err != nil {
		return nil, err
	}

	lockPath := filepath.Join(filepath.Dir(path), ".config.toml.lock")
	runlock, err := a.locker.RLock(lockPath)
	if err != nil {
		return nil, err
	}
	mcpMu.RLock()
	raw, err := readConfigTOML(path)
	mcpMu.RUnlock()
	runlock()
	if err != nil {
		return nil, err
	}

	servers := getMCPServersRaw(raw)
	out := make([]codeagent.MCPServer, 0, len(servers))
	for name, v := range servers {
		s, err := rawToMCPServer(name, v)
		if err != nil {
			logger.Warn("ListMCP: skip malformed entry", "name", name, "err", err)
			continue
		}
		out = append(out, s)
	}
	return &codeagent.ListMCPResult{Servers: out}, nil
}

func (a *codexAgent) DeleteMCP(p codeagent.DeleteMCPParams) (*codeagent.DeleteMCPResult, error) {
	path, err := a.configPathForMCP(p.Global)
	if err != nil {
		return nil, err
	}
	err = a.withMCPConfig(path, func(raw map[string]interface{}) error {
		servers := getMCPServersRaw(raw)
		delete(servers, p.Name)
		raw["mcp_servers"] = servers
		return nil
	})
	if err != nil {
		return nil, err
	}
	logger.Debug("DeleteMCP", "name", p.Name, "global", p.Global)
	return &codeagent.DeleteMCPResult{}, nil
}

func (a *codexAgent) SetMCPToolPrompt(p codeagent.SetMCPToolPromptParams) (*codeagent.SetMCPToolPromptResult, error) {
	path, err := a.configPathForMCP(p.Global)
	if err != nil {
		return nil, err
	}
	err = a.withMCPConfig(path, func(raw map[string]interface{}) error {
		servers := getMCPServersRaw(raw)
		srv, _ := servers[p.Prompt.ServerName].(map[string]interface{})
		if srv == nil {
			return fmt.Errorf("codex: set mcp tool prompt: server %q not found", p.Prompt.ServerName)
		}
		tools, _ := srv["tools"].(map[string]interface{})
		if tools == nil {
			tools = map[string]interface{}{}
		}
		toolCfg, _ := tools[p.Prompt.ToolName].(map[string]interface{})
		if toolCfg == nil {
			toolCfg = map[string]interface{}{}
		}
		toolCfg["prompt"] = p.Prompt.Prompt
		tools[p.Prompt.ToolName] = toolCfg
		srv["tools"] = tools
		servers[p.Prompt.ServerName] = srv
		raw["mcp_servers"] = servers
		return nil
	})
	if err != nil {
		return nil, err
	}
	logger.Debug("SetMCPToolPrompt", "server", p.Prompt.ServerName, "tool", p.Prompt.ToolName)
	return &codeagent.SetMCPToolPromptResult{}, nil
}
