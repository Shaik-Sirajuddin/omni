package gvisor

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sandboxcommon "github.com/Shaik-Sirajuddin/omni/sandbox/common"
	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	kjson "github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

//go:embed template/config.json
var templateConfigJSON []byte

var (
	gvisorArtifactSpecConfigPath    = "spec/sandbox.config.json"
	gvisorArtifactRuntimeConfigPath = "gvisor/config.json"
	gvisorProviderTemplateFilePath  = "config.json"
	gvisorBundleConfigFileName      = "config.json"
	gvisorBundleKeepFileName        = ".keep"
	gvisorBundleRootFSDirName       = "rootfs"
)

// ConfigSyncOptions carries the resolved paths that SyncBundleConfig needs.
// The Provisioner populates these before calling the transformer.
type ConfigSyncOptions struct {
	ConfigDir    string
	ArtifactsDir string
	// WorkDir is the agent workspace path, mounted read-write inside the sandbox.
	WorkDir string
	// BundleDir is the resolved OCI bundle directory.
	// Empty means the bundle is user-managed and config.json must not be overwritten.
	BundleDir string
}

type ConfigTransformer interface {
	ParseConfig(config *provider.Config) (*provider.ParsedSandboxConfig, error)
	SpecFromConfig(config *provider.Config, workDir string) (ociSpec, error)
	SyncBundleConfig(id string, config *provider.Config, opts ConfigSyncOptions) error
}

// defaultConfigTransformer is stateless — all context arrives via method params.
type defaultConfigTransformer struct{}

func (t *defaultConfigTransformer) Parse(config *provider.Config) (*provider.ParsedSandboxConfig, error) {
	sbx := &provider.Sandbox{Config: provider.CloneConfig(config)}
	return &provider.ParsedSandboxConfig{
		AllowWrite:  provider.SandboxAllowsWrite(sbx),
		AccessDirs:  provider.SandboxAccessDirs(sbx),
		BlockedDirs: provider.SandboxBlockedDirs(sbx),
	}, nil
}

func (t *defaultConfigTransformer) ParseConfig(config *provider.Config) (*provider.ParsedSandboxConfig, error) {
	return t.Parse(config)
}

func (t *defaultConfigTransformer) SpecFromConfig(config *provider.Config, workDir string) (ociSpec, error) {
	parsed, err := t.ParseConfig(config)
	if err != nil {
		return ociSpec{}, err
	}
	spec := defaultSpec()
	spec.Mounts = ociMountsFor(parsed, workDir)
	if len(parsed.BlockedDirs) > 0 {
		spec.Linux = &ociLinux{
			MaskedPaths: provider.UniqueCleaned(parsed.BlockedDirs),
		}
	}
	return spec, nil
}

func (t *defaultConfigTransformer) SyncBundleConfig(id string, config *provider.Config, opts ConfigSyncOptions) error {
	if err := validateSandboxID(id); err != nil {
		return err
	}
	spec, err := t.SpecFromConfig(config, opts.WorkDir)
	if err != nil {
		return err
	}
	if err := writeProvisionArtifacts(opts.ArtifactsDir, config, spec); err != nil {
		return err
	}
	raw, err := mergeSpecOntoTemplate(spec)
	if err != nil {
		return err
	}
	templatePath, err := sandboxcommon.WriteProviderTemplate(opts.ConfigDir, gvisorProviderTemplateFilePath, raw)
	if err != nil {
		return err
	}
	if strings.TrimSpace(templatePath) != "" {
		logger.Debug("gvisor provider template synced", "id", id, "path", templatePath)
	}
	if strings.TrimSpace(opts.BundleDir) == "" {
		// User-managed bundle: config.json must not be overwritten.
		return nil
	}
	if err := os.MkdirAll(filepath.Join(opts.BundleDir, gvisorBundleRootFSDirName), 0o755); err != nil {
		return fmt.Errorf("sandbox: create bundle rootfs dir: %w", err)
	}
	configPath := filepath.Join(opts.BundleDir, gvisorBundleConfigFileName)
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		return fmt.Errorf("sandbox: write gvisor spec: %w", err)
	}
	logger.Debug("gvisor bundle config synced", "id", id, "bundle", opts.BundleDir, "configPath", configPath)
	return nil
}

// mergeSpecOntoTemplate loads the embedded template config.json via koanf,
// overlays only the mounts and linux sections derived from sandbox.Config,
// and returns the merged JSON. Template fields such as linux.namespaces,
// process, and root are preserved.
func mergeSpecOntoTemplate(spec ociSpec) ([]byte, error) {
	k := koanf.New(".")
	if err := k.Load(rawbytes.Provider(templateConfigJSON), kjson.Parser()); err != nil {
		return nil, fmt.Errorf("sandbox: load gvisor template config: %w", err)
	}

	override := map[string]interface{}{}
	if len(spec.Mounts) > 0 {
		mounts := make([]interface{}, len(spec.Mounts))
		for i, m := range spec.Mounts {
			opts := make([]interface{}, len(m.Options))
			for j, o := range m.Options {
				opts[j] = o
			}
			mounts[i] = map[string]interface{}{
				"destination": m.Destination,
				"type":        m.Type,
				"source":      m.Source,
				"options":     opts,
			}
		}
		override["mounts"] = mounts
	}
	if spec.Linux != nil && len(spec.Linux.MaskedPaths) > 0 {
		linuxMap := map[string]interface{}{}
		for _, key := range k.MapKeys("linux") {
			linuxMap[key] = k.Get("linux." + key)
		}
		linuxMap["maskedPaths"] = spec.Linux.MaskedPaths
		override["linux"] = linuxMap
	}

	if len(override) > 0 {
		if err := k.Load(rawbytes.Provider(mustMarshalJSON(override)), kjson.Parser()); err != nil {
			return nil, fmt.Errorf("sandbox: apply gvisor config overrides: %w", err)
		}
	}

	raw, err := json.MarshalIndent(k.Raw(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("sandbox: marshal merged gvisor config: %w", err)
	}
	return raw, nil
}

func mustMarshalJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("sandbox: marshal override map: %v", err))
	}
	return b
}

func writeProvisionArtifacts(artifactsDir string, config *provider.Config, spec ociSpec) error {
	base := strings.TrimSpace(artifactsDir)
	if base == "" {
		return nil
	}
	rawConfig, err := marshalSandboxConfig(config)
	if err != nil {
		return err
	}
	if err := writeArtifactFile(base, gvisorArtifactSpecConfigPath, rawConfig); err != nil {
		return err
	}
	rawSpec, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("sandbox: marshal gvisor artifact spec: %w", err)
	}
	return writeArtifactFile(base, gvisorArtifactRuntimeConfigPath, rawSpec)
}

func validateSandboxID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("sandbox: id is required")
	}
	return nil
}
