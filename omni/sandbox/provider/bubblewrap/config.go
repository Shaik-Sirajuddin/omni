package bubblewrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	parserjson "github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Executable string           `json:"executable,omitempty" yaml:"executable,omitempty" koanf:"executable"`
	WorkDir    string           `json:"work_dir,omitempty" yaml:"work_dir,omitempty" koanf:"work_dir"`
	ExtraArgs  []string         `json:"extra_args,omitempty" yaml:"extra_args,omitempty" koanf:"extra_args"`
	Sandbox    FilesystemConfig `json:"sandbox,omitempty" yaml:"sandbox,omitempty" koanf:"sandbox"`
}

type FilesystemConfig struct {
	AllowWrite  bool     `json:"allow_write,omitempty" yaml:"allow_write,omitempty" koanf:"allow_write"`
	AccessDirs  []string `json:"access_dirs,omitempty" yaml:"access_dirs,omitempty" koanf:"access_dirs"`
	BlockedDirs []string `json:"blocked_dirs,omitempty" yaml:"blocked_dirs,omitempty" koanf:"blocked_dirs"`
}

type ConfigTransformer interface {
	FromSandbox(config *provider.Config, opts provider.ProvisionerOptions) (*Config, error)
	ToSandbox(config *Config) (*provider.Config, error)
}

type Store interface {
	Load(path string) (*Config, error)
	Save(path string, config *Config) error
}

type defaultConfigTransformer struct{}

type diskStore struct{}

func NewStore() Store {
	return diskStore{}
}

func (defaultConfigTransformer) FromSandbox(config *provider.Config, opts provider.ProvisionerOptions) (*Config, error) {
	sbx := &provider.Sandbox{Config: provider.CloneConfig(config)}
	out := &Config{
		Executable: strings.TrimSpace(opts.Executable),
		WorkDir:    strings.TrimSpace(opts.WorkDir),
		ExtraArgs:  append([]string{}, opts.ExtraArgs...),
		Sandbox: FilesystemConfig{
			AllowWrite:  provider.SandboxAllowsWrite(sbx),
			AccessDirs:  provider.UniqueCleaned(provider.SandboxAccessDirs(sbx)),
			BlockedDirs: provider.UniqueCleaned(provider.SandboxBlockedDirs(sbx)),
		},
	}
	return out, nil
}

func (defaultConfigTransformer) ToSandbox(config *Config) (*provider.Config, error) {
	if config == nil {
		return nil, fmt.Errorf("sandbox: bubblewrap config is required")
	}
	fsPolicy := provider.FSPolicy(provider.PermissiveRead)
	if config.Sandbox.AllowWrite {
		fsPolicy = provider.FSPolicy(provider.Inherit)
	}
	return &provider.Config{
		AgentPolicy: &provider.Policy{
			FSPolicy: fsPolicy,
			Config: provider.MountConfig{
				AccessDirs:  provider.UniqueCleaned(config.Sandbox.AccessDirs),
				BlockedDirs: provider.UniqueCleaned(config.Sandbox.BlockedDirs),
			},
		},
	}, nil
}

func (diskStore) Load(path string) (*Config, error) {
	filePath, err := resolveConfigPath(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("sandbox: read bubblewrap config %s: %w", filePath, err)
	}
	var payload any
	if err := yaml.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("sandbox: parse bubblewrap config %s: %w", filePath, err)
	}
	jsonRaw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("sandbox: convert bubblewrap config %s to json: %w", filePath, err)
	}
	k := koanf.New(".")
	if err := k.Load(rawbytes.Provider(jsonRaw), parserjson.Parser()); err != nil {
		return nil, fmt.Errorf("sandbox: load bubblewrap config %s: %w", filePath, err)
	}
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("sandbox: decode bubblewrap config %s: %w", filePath, err)
	}
	cfg.Sandbox.AccessDirs = provider.UniqueCleaned(cfg.Sandbox.AccessDirs)
	cfg.Sandbox.BlockedDirs = provider.UniqueCleaned(cfg.Sandbox.BlockedDirs)
	return &cfg, nil
}

func (diskStore) Save(path string, config *Config) error {
	if config == nil {
		return fmt.Errorf("sandbox: bubblewrap config is required")
	}
	filePath, err := resolveConfigPath(path)
	if err != nil {
		return err
	}
	k := koanf.New(".")
	jsonRaw, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("sandbox: marshal bubblewrap config: %w", err)
	}
	if err := k.Load(rawbytes.Provider(jsonRaw), parserjson.Parser()); err != nil {
		return fmt.Errorf("sandbox: load bubblewrap config: %w", err)
	}
	raw, err := yaml.Marshal(k.Raw())
	if err != nil {
		return fmt.Errorf("sandbox: encode bubblewrap config %s: %w", filePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("sandbox: create bubblewrap config dir %s: %w", filepath.Dir(filePath), err)
	}
	if err := os.WriteFile(filePath, raw, 0o644); err != nil {
		return fmt.Errorf("sandbox: write bubblewrap config %s: %w", filePath, err)
	}
	return nil
}

func resolveConfigPath(path string) (string, error) {
	filePath := strings.TrimSpace(path)
	if filePath == "" {
		return "", fmt.Errorf("sandbox: bubblewrap config path is required")
	}
	return filepath.Clean(filePath), nil
}
