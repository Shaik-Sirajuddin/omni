package agy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
)

// mcpFileMu guards all reads and writes to mcp_config.json for MCP state.
// It coordinates goroutines within this process; mtime checks handle cross-process races.
var mcpFileMu sync.RWMutex

// rawMCPServer is the JSON shape Gemini CLI stores in mcp_config.json under mcpServers.
type rawMCPServer struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

var errMtimeChanged = fmt.Errorf("mcp: settings file modified concurrently")

// geminiMCPConfigPath returns the MCP config path for global or workspace scope.
// Global: ~/.gemini/config/mcp_config.json
// Workspace: <workDir>/.gemini/config/mcp_config.json
func geminiMCPConfigPath(global bool, workDir string) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("agy: resolve home dir: %w", err)
		}
		return filepath.Join(home, ".gemini", "config", "mcp_config.json"), nil
	}
	return filepath.Join(workDir, ".gemini", "config", "mcp_config.json"), nil
}

func readMCPRaw(path string) (raw map[string]json.RawMessage, servers map[string]rawMCPServer, mtime time.Time, err error) {
	if info, statErr := os.Stat(path); statErr == nil {
		mtime = info.ModTime()
	}

	raw = map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) || len(data) == 0 {
		return raw, map[string]rawMCPServer{}, mtime, nil
	}
	if err != nil {
		return nil, nil, mtime, fmt.Errorf("read %s: %w", path, err)
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, nil, mtime, fmt.Errorf("decode %s: %w", path, err)
	}

	servers = map[string]rawMCPServer{}
	if blob, ok := raw["mcpServers"]; ok {
		if err = json.Unmarshal(blob, &servers); err != nil {
			return nil, nil, mtime, fmt.Errorf("decode mcpServers in %s: %w", path, err)
		}
	}
	return raw, servers, mtime, nil
}

func writeMCPRaw(path string, raw map[string]json.RawMessage, servers map[string]rawMCPServer, expectedMtime time.Time) error {
	if !expectedMtime.IsZero() {
		if info, statErr := os.Stat(path); statErr == nil && !info.ModTime().Equal(expectedMtime) {
			return errMtimeChanged
		}
	}

	blob, err := json.Marshal(servers)
	if err != nil {
		return fmt.Errorf("marshal servers: %w", err)
	}
	raw["mcpServers"] = blob

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp-*.tmp")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	return os.Rename(tmpName, path)
}

func withMCPWrite(op func() error) error {
	const maxRetries = 3
	for i := 0; i < maxRetries; i++ {
		mcpFileMu.Lock()
		err := op()
		mcpFileMu.Unlock()
		if err != errMtimeChanged {
			return err
		}
	}
	return fmt.Errorf("mcp: config file updated concurrently %d times, giving up", maxRetries)
}

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

func (a *agyAgent) AddMCP(p codeagent.AddMCPParams) (*codeagent.AddMCPResult, error) {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	slog.Debug("agy: AddMCP called", "name", p.Server.Name, "global", p.Global, "workDir", workDir, "transport", p.Server.Transport)

	path, err := geminiMCPConfigPath(p.Global, workDir)
	if err != nil {
		slog.Error("agy: AddMCP: resolve path failed", "name", p.Server.Name, "err", err)
		return nil, fmt.Errorf("agy: AddMCP: resolve path: %w", err)
	}
	slog.Debug("agy: AddMCP writing to", "path", path)
	if err := withMCPWrite(func() error {
		raw, servers, mtime, err := readMCPRaw(path)
		if err != nil {
			return err
		}
		servers[p.Server.Name] = mcpToRaw(p.Server)
		return writeMCPRaw(path, raw, servers, mtime)
	}); err != nil {
		slog.Error("agy: AddMCP write failed", "name", p.Server.Name, "path", path, "err", err)
		return nil, fmt.Errorf("agy: AddMCP %q: %w", p.Server.Name, err)
	}
	slog.Info("agy: AddMCP success", "name", p.Server.Name, "path", path)
	return &codeagent.AddMCPResult{}, nil
}

func (a *agyAgent) ListMCP(p codeagent.ListMCPParams) (*codeagent.ListMCPResult, error) {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	path, err := geminiMCPConfigPath(p.Global, workDir)
	if err != nil {
		return nil, fmt.Errorf("agy: ListMCP: resolve path: %w", err)
	}
	mcpFileMu.RLock()
	_, servers, _, err := readMCPRaw(path)
	mcpFileMu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("agy: ListMCP: %w", err)
	}
	result := make([]codeagent.MCPServer, 0, len(servers))
	for name, r := range servers {
		result = append(result, rawToMCP(name, r))
	}
	return &codeagent.ListMCPResult{Servers: result}, nil
}

func (a *agyAgent) DeleteMCP(p codeagent.DeleteMCPParams) (*codeagent.DeleteMCPResult, error) {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	path, err := geminiMCPConfigPath(p.Global, workDir)
	if err != nil {
		return nil, fmt.Errorf("agy: DeleteMCP: resolve path: %w", err)
	}
	if err := withMCPWrite(func() error {
		raw, servers, mtime, err := readMCPRaw(path)
		if err != nil {
			return err
		}
		delete(servers, p.Name)
		return writeMCPRaw(path, raw, servers, mtime)
	}); err != nil {
		return nil, fmt.Errorf("agy: DeleteMCP %q: %w", p.Name, err)
	}
	return &codeagent.DeleteMCPResult{}, nil
}

func (a *agyAgent) SetMCPToolPrompt(_ codeagent.SetMCPToolPromptParams) (*codeagent.SetMCPToolPromptResult, error) {
	return nil, fmt.Errorf("agy: SetMCPToolPrompt not supported")
}
