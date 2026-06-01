package common

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

const (
	CommonConfigFile = "config.json"
	GenDir           = "gen"
)

// DefaultAgentTemplate returns the baseline sandbox config used when no valid
// common config is available in ConfigDir.
func DefaultAgentTemplate() *provider.Config {
	return &provider.Config{
		WorkSpacePolicy: &provider.Policy{
			Dir:      provider.WorkspaceDir("/workspace"),
			FSPolicy: provider.FSPolicy(provider.PermissiveRead),
			Config: provider.MountConfig{
				AccessDirs:  []string{"/workspace", "/usr/bin"},
				BlockedDirs: []string{"/workspace/.git"},
			},
		},
		AgentPolicy: &provider.Policy{
			Dir:      provider.Default,
			FSPolicy: provider.FSPolicy(provider.Inherit),
			Config: provider.MountConfig{
				AccessDirs:  []string{"/tmp"},
				BlockedDirs: []string{"/root", "/etc/shadow"},
			},
		},
	}
}

// AgentDefaultConfig returns a sandbox config scoped to the given workspaceDir.
// The workspace policy grants permissive-read access to the actual directory,
// blocking the .git subdirectory. This replaces the hardcoded /workspace path
// in DefaultAgentTemplate for cases where the real workspace is known.
func AgentDefaultConfig(workspaceDir string) *provider.Config {
	dir := workspaceDir
	if dir == "" {
		dir = "/workspace"
	}
	return &provider.Config{
		WorkSpacePolicy: &provider.Policy{
			Dir:      provider.WorkspaceDir(dir),
			FSPolicy: provider.FSPolicy(provider.PermissiveRead),
			Config: provider.MountConfig{
				AccessDirs:  []string{dir, "/usr/bin"},
				BlockedDirs: []string{filepath.Join(dir, ".git")},
			},
		},
		AgentPolicy: &provider.Policy{
			Dir:      provider.Default,
			FSPolicy: provider.FSPolicy(provider.Inherit),
			Config: provider.MountConfig{
				AccessDirs:  []string{"/tmp"},
				BlockedDirs: []string{"/root", "/etc/shadow"},
			},
		},
	}
}

// EnsureCommonConfig loads ConfigDir/config.json when present, or creates it
// from fallback/default content when missing. The returned path is empty when
// configDir is not provided.
func EnsureCommonConfig(configDir string, parser provider.ConfigFileParser, fallback *provider.Config) (*provider.Config, string, error) {
	cleanDir := strings.TrimSpace(configDir)
	if cleanDir == "" {
		cfg := provider.CloneConfig(fallback)
		if cfg == nil {
			cfg = DefaultAgentTemplate()
		}
		if parser != nil {
			if err := parser.Validate(cfg); err != nil {
				return nil, "", err
			}
		}
		return cfg, "", nil
	}
	if parser == nil {
		return nil, "", fmt.Errorf("sandbox: config parser is required for config dir provisioning")
	}
	cleanDir = filepath.Clean(cleanDir)
	path := filepath.Join(cleanDir, CommonConfigFile)
	if _, err := os.Stat(path); err == nil {
		cfg, loadErr := parser.Load(path)
		if loadErr != nil {
			return nil, "", loadErr
		}
		if validateErr := parser.Validate(cfg); validateErr != nil {
			return nil, "", validateErr
		}
		return cfg, path, nil
	} else if !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("sandbox: stat common config %s: %w", path, err)
	}
	cfg := provider.CloneConfig(fallback)
	if cfg == nil {
		cfg = DefaultAgentTemplate()
	}
	if err := parser.Validate(cfg); err != nil {
		return nil, "", err
	}
	if err := parser.Save(cfg, path); err != nil {
		return nil, "", err
	}
	return cfg, path, nil
}

// SyncCommonConfig validates and persists ConfigDir/config.json from the
// runtime's latest config value. The returned path is empty when configDir is
// not provided.
func SyncCommonConfig(configDir string, parser provider.ConfigFileParser, config *provider.Config) (*provider.Config, string, error) {
	cfg := provider.CloneConfig(config)
	if cfg == nil {
		cfg = DefaultAgentTemplate()
	}
	if parser == nil {
		return nil, "", fmt.Errorf("sandbox: config parser is required for config dir provisioning")
	}
	if err := parser.Validate(cfg); err != nil {
		return nil, "", err
	}

	cleanDir := strings.TrimSpace(configDir)
	if cleanDir == "" {
		return cfg, "", nil
	}
	path := filepath.Join(filepath.Clean(cleanDir), CommonConfigFile)
	if err := parser.Save(cfg, path); err != nil {
		return nil, "", err
	}
	return cfg, path, nil
}

// WriteProviderTemplate stores provisioner-specific generated content under
// ConfigDir/gen/<fileName>.
func WriteProviderTemplate(configDir string, fileName string, payload []byte) (string, error) {
	cleanDir := strings.TrimSpace(configDir)
	if cleanDir == "" {
		return "", nil
	}
	if strings.TrimSpace(fileName) == "" {
		return "", fmt.Errorf("sandbox: provider template file name is required")
	}
	cleanDir = filepath.Clean(cleanDir)
	genDir := filepath.Join(cleanDir, GenDir)
	target := filepath.Join(genDir, fileName)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("sandbox: create provisioner template dir %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, payload, 0o644); err != nil {
		return "", fmt.Errorf("sandbox: write provisioner template %s: %w", target, err)
	}
	return target, nil
}
