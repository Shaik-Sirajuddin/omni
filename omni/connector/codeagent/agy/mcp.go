package agy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
)

var (
	mcpJsonMu     sync.RWMutex
	agySettingsMu sync.RWMutex
)

// rawMCPServer is the JSON shape for mcp.json and agy settings.json.
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

func mcpDotJsonPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mcp.json"), nil
}

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

	if len(servers) > 0 {
		blob, err := json.Marshal(servers)
		if err != nil {
			return fmt.Errorf("marshal servers: %w", err)
		}
		raw["mcpServers"] = blob
	} else {
		delete(raw, "mcpServers")
	}

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

func readAgySettingsRaw(path string) (raw map[string]json.RawMessage, enabled []string, mtime time.Time, err error) {
	if info, statErr := os.Stat(path); statErr == nil {
		mtime = info.ModTime()
	}

	raw = map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return raw, []string{}, mtime, nil
	}
	if err != nil {
		return nil, nil, mtime, fmt.Errorf("read %s: %w", path, err)
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, nil, mtime, fmt.Errorf("decode %s: %w", path, err)
	}

	enabled = []string{}
	if blob, ok := raw["enabledMcpjsonServers"]; ok {
		if err = json.Unmarshal(blob, &enabled); err != nil {
			return nil, nil, mtime, fmt.Errorf("decode enabledMcpjsonServers in %s: %w", path, err)
		}
	}
	return raw, enabled, mtime, nil
}

func writeAgySettingsRaw(path string, raw map[string]json.RawMessage, enabled []string, expectedMtime time.Time) error {
	if !expectedMtime.IsZero() {
		if info, statErr := os.Stat(path); statErr == nil && !info.ModTime().Equal(expectedMtime) {
			return errMtimeChanged
		}
	}

	if len(enabled) > 0 {
		blob, err := json.Marshal(enabled)
		if err != nil {
			return fmt.Errorf("marshal enabledMcpjsonServers: %w", err)
		}
		raw["enabledMcpjsonServers"] = blob
	} else {
		delete(raw, "enabledMcpjsonServers")
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
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

func withRetry(mu *sync.RWMutex, op func() error) error {
	const maxRetries = 3
	for i := 0; i < maxRetries; i++ {
		mu.Lock()
		err := op()
		mu.Unlock()
		if err != errMtimeChanged {
			return err
		}
	}
	return fmt.Errorf("file updated concurrently %d times, giving up", maxRetries)
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

func (a *agyAgent) AddMCP(p codeagent.AddMCPParams) (*codeagent.AddMCPResult, error) {
	var settingsPath string
	if p.Global {
		sp, err := agyUserSettingsPath()
		if err != nil {
			return nil, err
		}
		settingsPath = sp
	} else {
		a.mu.RLock()
		settingsPath = agyWorkspaceSettingsPath(a.workDir)
		a.mu.RUnlock()
	}

	if p.Global {
		err := withRetry(&agySettingsMu, func() error {
			raw, enabled, mtime, err := readAgySettingsRaw(settingsPath)
			if err != nil {
				return err
			}
			servers := map[string]rawMCPServer{}
			if blob, ok := raw["mcpServers"]; ok {
				json.Unmarshal(blob, &servers)
			}
			servers[p.Server.Name] = mcpToRaw(p.Server)
			if blob, err := json.Marshal(servers); err == nil {
				raw["mcpServers"] = blob
			}
			// enabledMcpjsonServers is only for ~/.mcp.json servers; inline
			// mcpServers written above are always active without an enable entry.
			return writeAgySettingsRaw(settingsPath, raw, enabled, mtime)
		})
		if err != nil {
			return nil, fmt.Errorf("agy: AddMCP global: %w", err)
		}
	} else {
		mcpPath, err := mcpDotJsonPath()
		if err != nil {
			return nil, err
		}
		err = withRetry(&mcpJsonMu, func() error {
			raw, servers, mtime, err := readMCPRaw(mcpPath)
			if err != nil {
				return err
			}
			servers[p.Server.Name] = mcpToRaw(p.Server)
			return writeMCPRaw(mcpPath, raw, servers, mtime)
		})
		if err != nil {
			return nil, fmt.Errorf("agy: AddMCP local (mcp.json): %w", err)
		}

		err = withRetry(&agySettingsMu, func() error {
			raw, enabled, mtime, err := readAgySettingsRaw(settingsPath)
			if err != nil {
				return err
			}
			found := false
			for _, n := range enabled {
				if n == p.Server.Name {
					found = true
					break
				}
			}
			if !found {
				enabled = append(enabled, p.Server.Name)
			}
			return writeAgySettingsRaw(settingsPath, raw, enabled, mtime)
		})
		if err != nil {
			return nil, fmt.Errorf("agy: AddMCP local (settings.json): %w", err)
		}
	}

	return &codeagent.AddMCPResult{}, nil
}

func (a *agyAgent) ListMCP(p codeagent.ListMCPParams) (*codeagent.ListMCPResult, error) {
	var servers map[string]rawMCPServer

	if p.Global {
		path, err := agyUserSettingsPath()
		if err != nil {
			return nil, err
		}
		agySettingsMu.RLock()
		raw, _, _, err := readAgySettingsRaw(path)
		agySettingsMu.RUnlock()
		if err != nil {
			return nil, fmt.Errorf("agy: ListMCP global: %w", err)
		}
		servers = map[string]rawMCPServer{}
		if blob, ok := raw["mcpServers"]; ok {
			json.Unmarshal(blob, &servers)
		}
	} else {
		path, err := mcpDotJsonPath()
		if err != nil {
			return nil, err
		}
		mcpJsonMu.RLock()
		_, srvs, _, err := readMCPRaw(path)
		mcpJsonMu.RUnlock()
		if err != nil {
			return nil, fmt.Errorf("agy: ListMCP local: %w", err)
		}
		servers = srvs
	}

	result := make([]codeagent.MCPServer, 0, len(servers))
	for name, r := range servers {
		result = append(result, rawToMCP(name, r))
	}
	return &codeagent.ListMCPResult{Servers: result}, nil
}

func (a *agyAgent) DeleteMCP(p codeagent.DeleteMCPParams) (*codeagent.DeleteMCPResult, error) {
	var settingsPath string
	if p.Global {
		sp, err := agyUserSettingsPath()
		if err != nil {
			return nil, err
		}
		settingsPath = sp
	} else {
		a.mu.RLock()
		settingsPath = agyWorkspaceSettingsPath(a.workDir)
		a.mu.RUnlock()
	}

	if p.Global {
		err := withRetry(&agySettingsMu, func() error {
			raw, enabled, mtime, err := readAgySettingsRaw(settingsPath)
			if err != nil {
				return err
			}
			servers := map[string]rawMCPServer{}
			if blob, ok := raw["mcpServers"]; ok {
				json.Unmarshal(blob, &servers)
			}
			delete(servers, p.Name)
			if len(servers) > 0 {
				blob, _ := json.Marshal(servers)
				raw["mcpServers"] = blob
			} else {
				delete(raw, "mcpServers")
			}
			// Pass enabled unchanged — inline mcpServers don't use enabledMcpjsonServers.
			return writeAgySettingsRaw(settingsPath, raw, enabled, mtime)
		})
		if err != nil {
			return nil, fmt.Errorf("agy: DeleteMCP global: %w", err)
		}
	} else {
		mcpPath, err := mcpDotJsonPath()
		if err != nil {
			return nil, err
		}
		err = withRetry(&mcpJsonMu, func() error {
			raw, servers, mtime, err := readMCPRaw(mcpPath)
			if err != nil {
				return err
			}
			delete(servers, p.Name)
			return writeMCPRaw(mcpPath, raw, servers, mtime)
		})
		if err != nil {
			return nil, fmt.Errorf("agy: DeleteMCP local (mcp.json): %w", err)
		}

		err = withRetry(&agySettingsMu, func() error {
			raw, enabled, mtime, err := readAgySettingsRaw(settingsPath)
			if err != nil {
				return err
			}
			newEnabled := make([]string, 0, len(enabled))
			for _, n := range enabled {
				if n != p.Name {
					newEnabled = append(newEnabled, n)
				}
			}
			return writeAgySettingsRaw(settingsPath, raw, newEnabled, mtime)
		})
		if err != nil {
			return nil, fmt.Errorf("agy: DeleteMCP local (settings.json): %w", err)
		}
	}

	return &codeagent.DeleteMCPResult{}, nil
}

func (a *agyAgent) SetMCPToolPrompt(p codeagent.SetMCPToolPromptParams) (*codeagent.SetMCPToolPromptResult, error) {
	return nil, fmt.Errorf("agy: SetMCPToolPrompt not supported")
}
