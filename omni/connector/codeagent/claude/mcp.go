package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
)

// mcpPathMu maps an absolute settings.json path to its own RWMutex so that
// agents in different worktrees don't block each other.
var mcpPathMu sync.Map // map[string]*sync.RWMutex

func mcpMutexFor(path string) *sync.RWMutex {
	v, _ := mcpPathMu.LoadOrStore(path, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}

// NOTE: MCP path resolution below is based on the Claude Code scope table and has
// NOT been tested against a live Claude installation. Verify that ~/.claude.json
// and <workDir>/.mcp.json match the actual files Claude Code reads before relying
// on AddMCP/ListMCP/DeleteMCP in production.

// mcpUserSettingsPath returns the path to the user-global MCP config file.
// Claude Code stores user-scoped MCP servers in ~/.claude.json.
func mcpUserSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("mcp: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude.json"), nil
}

// mcpWorkspaceSettingsPath returns the per-worktree MCP config path.
// Claude Code stores project-scoped MCP servers in .mcp.json at the project root,
// so each worktree (workDir) gets its own isolated MCP config.
func mcpWorkspaceSettingsPath(workDir string) string {
	return filepath.Join(workDir, ".mcp.json")
}

// rawMCPServer is the JSON shape Claude Code stores in settings.json under mcpServers.
type rawMCPServer struct {
	Type    string                      `json:"type,omitempty"`
	Command string                      `json:"command,omitempty"`
	Args    []string                    `json:"args,omitempty"`
	URL     string                      `json:"url,omitempty"`
	Env     map[string]string           `json:"env,omitempty"`
	Headers map[string]string           `json:"headers,omitempty"`
	Timeout int                         `json:"timeout,omitempty"`
	Tools   map[string]rawMCPToolConfig `json:"tools,omitempty"`
}

type rawMCPToolConfig struct {
	Prompt string `json:"prompt,omitempty"`
}

// errMtimeChanged is returned by writeMCPRaw when the settings file was modified
// by another process between the read and write. Callers should retry.
var errMtimeChanged = fmt.Errorf("mcp: settings file modified concurrently")

func (a *claudeAgent) mcpSettingsPath(global bool) (string, error) {
	if global {
		return mcpUserSettingsPath()
	}
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	return mcpWorkspaceSettingsPath(workDir), nil
}

// readMCPRaw reads the settings file at path and returns:
//   - raw: the full JSON map (preserves unrelated keys on write)
//   - servers: the parsed mcpServers sub-map
//   - mtime: file modification time at read time (zero if file absent)
func readMCPRaw(path string) (raw map[string]json.RawMessage, servers map[string]rawMCPServer, mtime time.Time, err error) {
	if info, statErr := os.Stat(path); statErr == nil {
		mtime = info.ModTime()
	}

	raw = map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return raw, map[string]rawMCPServer{}, mtime, nil
	}
	if err != nil {
		return nil, nil, mtime, fmt.Errorf("mcp: read %s: %w", path, err)
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, nil, mtime, fmt.Errorf("mcp: decode %s: %w", path, err)
	}

	servers = map[string]rawMCPServer{}
	if blob, ok := raw["mcpServers"]; ok {
		if err = json.Unmarshal(blob, &servers); err != nil {
			return nil, nil, mtime, fmt.Errorf("mcp: decode mcpServers in %s: %w", path, err)
		}
	}
	return raw, servers, mtime, nil
}

// writeMCPRaw merges servers back into raw and writes to path atomically (temp + rename).
// Returns errMtimeChanged when another process has modified the file since expectedMtime.
func writeMCPRaw(path string, raw map[string]json.RawMessage, servers map[string]rawMCPServer, expectedMtime time.Time) error {
	// Cross-process mtime guard.
	if !expectedMtime.IsZero() {
		if info, statErr := os.Stat(path); statErr == nil && !info.ModTime().Equal(expectedMtime) {
			return errMtimeChanged
		}
	}

	blob, err := json.Marshal(servers)
	if err != nil {
		return fmt.Errorf("mcp: marshal servers: %w", err)
	}
	raw["mcpServers"] = blob

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("mcp: marshal settings: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mcp: mkdir %s: %w", filepath.Dir(path), err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-mcp-*.tmp")
	if err != nil {
		return fmt.Errorf("mcp: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("mcp: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("mcp: close temp: %w", err)
	}
	return os.Rename(tmpName, path)
}

// withMCPWrite runs op under the write lock for path, retrying up to 3 times
// when writeMCPRaw returns errMtimeChanged (cross-process concurrent write).
// Using a per-path mutex means agents in different worktrees don't block each other.
func withMCPWrite(path string, op func() error) error {
	const maxRetries = 3
	mu := mcpMutexFor(path)
	for i := 0; i < maxRetries; i++ {
		mu.Lock()
		err := op()
		mu.Unlock()
		if err != errMtimeChanged {
			return err
		}
	}
	return fmt.Errorf("mcp: settings file updated concurrently %d times, giving up", maxRetries)
}

// mcpToRaw converts a codeagent.MCPServer to its settings.json shape.
func mcpToRaw(s codeagent.MCPServer) rawMCPServer {
	r := rawMCPServer{
		Env:     s.Env,
		Headers: s.Headers,
		Timeout: s.Timeout,
	}
	switch s.Transport {
	case codeagent.MCPTransportSSE:
		r.Type = "sse"
		r.URL = s.URL
	case codeagent.MCPTransportHTTP:
		r.Type = "http"
		r.URL = s.URL
	default:
		r.Type = "stdio"
		r.Command = s.Command
		r.Args = s.Args
	}
	return r
}

// rawToMCP converts a raw settings entry to a codeagent.MCPServer.
func rawToMCP(name string, r rawMCPServer) codeagent.MCPServer {
	s := codeagent.MCPServer{
		Name:    name,
		Env:     r.Env,
		Headers: r.Headers,
		Timeout: r.Timeout,
	}
	switch r.Type {
	case "sse":
		s.Transport = codeagent.MCPTransportSSE
		s.URL = r.URL
	case "http":
		s.Transport = codeagent.MCPTransportHTTP
		s.URL = r.URL
	default:
		s.Transport = codeagent.MCPTransportStdio
		s.Command = r.Command
		s.Args = r.Args
	}
	return s
}

// ============================================================
// MCPManager implementation
// ============================================================

func (a *claudeAgent) AddMCP(p codeagent.AddMCPParams) (*codeagent.AddMCPResult, error) {
	path, err := a.mcpSettingsPath(p.Global)
	if err != nil {
		return nil, fmt.Errorf("claude: AddMCP: resolve path: %w", err)
	}
	if err := withMCPWrite(path, func() error {
		raw, servers, mtime, err := readMCPRaw(path)
		if err != nil {
			return err
		}
		servers[p.Server.Name] = mcpToRaw(p.Server)
		return writeMCPRaw(path, raw, servers, mtime)
	}); err != nil {
		return nil, fmt.Errorf("claude: AddMCP %q: %w", p.Server.Name, err)
	}
	return &codeagent.AddMCPResult{}, nil
}

func (a *claudeAgent) ListMCP(p codeagent.ListMCPParams) (*codeagent.ListMCPResult, error) {
	path, err := a.mcpSettingsPath(p.Global)
	if err != nil {
		return nil, fmt.Errorf("claude: ListMCP: resolve path: %w", err)
	}
	mu := mcpMutexFor(path)
	mu.RLock()
	_, servers, _, err := readMCPRaw(path)
	mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("claude: ListMCP: %w", err)
	}
	result := make([]codeagent.MCPServer, 0, len(servers))
	for name, r := range servers {
		result = append(result, rawToMCP(name, r))
	}
	return &codeagent.ListMCPResult{Servers: result}, nil
}

func (a *claudeAgent) DeleteMCP(p codeagent.DeleteMCPParams) (*codeagent.DeleteMCPResult, error) {
	path, err := a.mcpSettingsPath(p.Global)
	if err != nil {
		return nil, fmt.Errorf("claude: DeleteMCP: resolve path: %w", err)
	}
	if err := withMCPWrite(path, func() error {
		raw, servers, mtime, err := readMCPRaw(path)
		if err != nil {
			return err
		}
		delete(servers, p.Name)
		return writeMCPRaw(path, raw, servers, mtime)
	}); err != nil {
		return nil, fmt.Errorf("claude: DeleteMCP %q: %w", p.Name, err)
	}
	return &codeagent.DeleteMCPResult{}, nil
}

func (a *claudeAgent) SetMCPToolPrompt(p codeagent.SetMCPToolPromptParams) (*codeagent.SetMCPToolPromptResult, error) {
	path, err := a.mcpSettingsPath(p.Global)
	if err != nil {
		return nil, fmt.Errorf("claude: SetMCPToolPrompt: resolve path: %w", err)
	}
	if err := withMCPWrite(path, func() error {
		raw, servers, mtime, err := readMCPRaw(path)
		if err != nil {
			return err
		}
		srv, ok := servers[p.Prompt.ServerName]
		if !ok {
			return fmt.Errorf("server %q not found", p.Prompt.ServerName)
		}
		if srv.Tools == nil {
			srv.Tools = map[string]rawMCPToolConfig{}
		}
		srv.Tools[p.Prompt.ToolName] = rawMCPToolConfig{Prompt: p.Prompt.Prompt}
		servers[p.Prompt.ServerName] = srv
		return writeMCPRaw(path, raw, servers, mtime)
	}); err != nil {
		return nil, fmt.Errorf("claude: SetMCPToolPrompt %q.%q: %w", p.Prompt.ServerName, p.Prompt.ToolName, err)
	}
	return &codeagent.SetMCPToolPromptResult{}, nil
}
