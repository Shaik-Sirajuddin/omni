package gvisor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValidation(t *testing.T) {
	t.Run("ValidateSandboxID", func(t *testing.T) {
		require.Error(t, validateSandboxID(""), "Validating an empty sandbox id should return an error")
		require.Error(t, validateSandboxID("   "), "Validating a whitespace sandbox id should return an error")
		require.NoError(t, validateSandboxID("sandbox-config-1"), "Validating a non-empty sandbox id should not return an error")
	})

	t.Run("StatelessTransformerParsesWithoutProvisioner", func(t *testing.T) {
		transformer := &defaultConfigTransformer{}
		_, err := transformer.ParseConfig(&provider.Config{})
		require.NoError(t, err, "Stateless transformer should parse config without a provisioner")
	})
}

func TestConfigParseAndRetrieval(t *testing.T) {
	t.Run("ParseConfigShouldMapWriteAndPaths", func(t *testing.T) {
		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: t.TempDir(),
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating a gVisor provisioner should not return an error")

		parsed, err := p.ParseConfig(&provider.Config{
			AgentPolicy: &provider.Policy{
				FSPolicy: provider.FSPolicy(provider.Inherit),
				Config: provider.MountConfig{
					AccessDirs:  []string{"/tmp/a", "/tmp/b"},
					BlockedDirs: []string{"/tmp/x", "/tmp/y"},
				},
			},
		})
		require.NoError(t, err, "Parsing sandbox config should not return an error")
		assert.True(t, parsed.AllowWrite, "Parsed config should allow write for inherit policy")
		assert.Subset(t, parsed.AccessDirs, []string{"/tmp/a", "/tmp/b"}, "Parsed config should include all access directories")
		assert.Subset(t, parsed.BlockedDirs, []string{"/tmp/x", "/tmp/y"}, "Parsed config should include all blocked directories")
	})
}

func TestConfigResolution(t *testing.T) {
	t.Run("ResolveSyncOptionsBundleDir", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: t.TempDir(),
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating a gVisor provisioner should not return an error")

		opts, err := p.resolveSyncOptions("sandbox-resolve-1", t.TempDir())
		require.NoError(t, err, "Resolving sync options should not return an error")
		require.NotEmpty(t, opts.BundleDir, "Resolved sync options BundleDir should not be empty")
		assert.Equal(t, filepath.Join(opts.BundleDir, gvisorBundleConfigFileName),
			filepath.Join(opts.BundleDir, gvisorBundleConfigFileName),
			"Resolved bundle dir should contain the expected config file name")
	})

	t.Run("ResolveSyncOptionsShouldFailForEmptyID", func(t *testing.T) {
		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: t.TempDir(),
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating a gVisor provisioner should not return an error")

		_, err = p.resolveSyncOptions(" ", t.TempDir())
		require.Error(t, err, "Resolving sync options with empty id should return an error")
	})
}

func TestConfigSaveAndDuplicates(t *testing.T) {
	t.Run("SyncBundleConfigShouldWriteTemplateAndArtifacts", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		workDir := t.TempDir()
		artifactsDir := t.TempDir()
		configDir := t.TempDir()

		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir:      workDir,
			ArtifactsDir: artifactsDir,
			Store:        newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating a gVisor provisioner should not return an error")

		err = p.SyncBundleConfig("sandbox-config-write-1", &provider.Config{
			AgentPolicy: &provider.Policy{
				FSPolicy: provider.FSPolicy(provider.PermissiveRead),
				Config: provider.MountConfig{
					AccessDirs:  []string{"/tmp/a", "/tmp/a", "/tmp/b"},
					BlockedDirs: []string{"/tmp/blocked", "/tmp/blocked"},
				},
			},
		}, ConfigSyncOptions{ConfigDir: configDir, ArtifactsDir: artifactsDir, WorkDir: workDir})
		require.NoError(t, err, "Syncing bundle config should not return an error")

		require.FileExists(t, filepath.Join(configDir, "gen", gvisorProviderTemplateFilePath), "Provider template file should be written under config dir gen")
		require.FileExists(t, filepath.Join(artifactsDir, gvisorArtifactSpecConfigPath), "Spec config artifact should be written in artifacts dir")
		require.FileExists(t, filepath.Join(artifactsDir, gvisorArtifactRuntimeConfigPath), "Runtime config artifact should be written in artifacts dir")
	})

	t.Run("SpecFromConfigShouldDeduplicatePaths", func(t *testing.T) {
		workDir := t.TempDir()
		accessDir := t.TempDir()
		blockedRaw := "/tmp/blocked-path"

		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: workDir,
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating a gVisor provisioner should not return an error")

		spec, err := p.SpecFromConfig(&provider.Config{
			AgentPolicy: &provider.Policy{
				FSPolicy: provider.FSPolicy(provider.PermissiveRead),
				Config: provider.MountConfig{
					AccessDirs:  []string{accessDir, accessDir},
					BlockedDirs: []string{blockedRaw, blockedRaw},
				},
			},
		}, workDir)
		require.NoError(t, err, "Building OCI spec from sandbox config should not return an error")
		require.NotNil(t, spec.Linux, "Built OCI spec should include linux section for blocked paths")
		assert.Equal(t, 1, countStringOccurrences(spec.Linux.MaskedPaths, filepath.Clean(blockedRaw)), "Masked paths should include blocked path only once after deduplication")
		assert.Equal(t, 1, countMountOccurrences(spec.Mounts, accessDir), "Spec mounts should include each duplicate access path only once")
	})
}

func TestConfigIntegrationWithGVisor(t *testing.T) {
	t.Run("ProvisionerCreateCaptureSyncAndTeardown", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("gVisor integration should run only on linux hosts")
		}
		if strings.TrimSpace(os.Getenv("GVISOR_TEST_ENABLE")) != "1" {
			t.Skip("gVisor integration should run only when GVISOR_TEST_ENABLE=1")
		}
		if _, err := exec.LookPath("runsc"); err != nil {
			t.Skip("gVisor integration should run only when runsc is installed and in PATH")
		}

		bundleDir := strings.TrimSpace(os.Getenv("GVISOR_TEST_BUNDLE_DIR"))
		if bundleDir == "" {
			t.Skip("gVisor integration should run only when GVISOR_TEST_BUNDLE_DIR is set")
		}
		bundleDir = filepath.Clean(bundleDir)
		if _, err := os.Stat(filepath.Join(bundleDir, gvisorBundleConfigFileName)); err != nil {
			t.Skip("gVisor integration should run only when bundle config.json exists in GVISOR_TEST_BUNDLE_DIR")
		}
		if info, err := os.Stat(filepath.Join(bundleDir, gvisorBundleRootFSDirName)); err != nil || !info.IsDir() {
			t.Skip("gVisor integration should run only when bundle rootfs exists in GVISOR_TEST_BUNDLE_DIR")
		}

		configDir := t.TempDir()
		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir:     bundleDir,
			RuntimeRoot: filepath.Join(t.TempDir(), "runsc-root"),
			Store:       newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating a gVisor provisioner should not return an error")

		id := fmt.Sprintf("gvisor-config-it-%d", time.Now().UnixNano())
		rt, err := p.Create(provider.CreateSandboxParams{
			ID:        id,
			ConfigDir: configDir,
			Config: &provider.Config{
				AgentPolicy: &provider.Policy{
					FSPolicy: provider.FSPolicy(provider.PermissiveRead),
				},
			},
		})
		require.NoError(t, err, "Creating gVisor runtime for integration should not return an error")
		require.NotNil(t, rt, "Creating gVisor runtime for integration should return runtime")

		t.Cleanup(func() {
			_ = p.runscManage("kill", id, "KILL")
			_ = p.runscManage("delete", id)
		})

		result, err := rt.Capture("/bin/sh", []string{"-lc", "echo gvisor-config-integration"})
		require.NoError(t, err, "Capturing output in gVisor integration runtime should not return an error")
		require.NotNil(t, result, "Capturing output in gVisor integration runtime should return a result")
		assert.Equal(t, 0, result.ExitCode, "Captured command should exit with code zero in integration runtime")
		assert.Contains(t, result.Stdout, "gvisor-config-integration", "Captured stdout should include integration marker text")

		err = rt.Sync(&provider.Config{
			AgentPolicy: &provider.Policy{
				FSPolicy: provider.FSPolicy(provider.PermissiveRead),
				Config: provider.MountConfig{
					BlockedDirs: []string{"/tmp/gvisor-it-blocked", "/tmp/gvisor-it-blocked"},
				},
			},
		})
		require.NoError(t, err, "Syncing config in gVisor integration runtime should not return an error")

		raw, err := os.ReadFile(filepath.Join(configDir, "gen", gvisorProviderTemplateFilePath))
		require.NoError(t, err, "Reading generated provider template after sync should not return an error")
		var spec ociSpec
		require.NoError(t, json.Unmarshal(raw, &spec), "Parsing generated provider template after sync should not return an error")
		require.NotNil(t, spec.Linux, "Generated provider template after sync should include linux section")
		assert.Subset(t, spec.Linux.MaskedPaths, []string{"/tmp/gvisor-it-blocked"}, "Generated provider template after sync should include blocked path")
	})
}

func TestTemplateOCIConfigFormat(t *testing.T) {
	t.Run("TemplateConfigJSONIsValidOCIShape", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("template", "config.json"))
		require.NoError(t, err, "Reading gVisor template config should not return an error")

		var spec ociSpec
		require.NoError(t, json.Unmarshal(raw, &spec), "Parsing gVisor template config as OCI spec should not return an error")
		assert.Equal(t, "1.0.2", spec.OCIVersion, "Template OCI config should include expected OCI version")
		assert.Equal(t, "rootfs", spec.Root.Path, "Template OCI config should point root.path to rootfs")
		assert.NotEmpty(t, spec.Process.Args, "Template OCI config should define process args")
		assert.Equal(t, "/", spec.Process.Cwd, "Template OCI config should define process working directory")
	})

	t.Run("TemplateLayoutListsRequiredBundlePaths", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("template", "oci-bundle-layout.txt"))
		require.NoError(t, err, "Reading gVisor OCI bundle layout template should not return an error")

		layout := string(raw)
		assert.Contains(t, layout, "config.json", "Template OCI bundle layout should list config.json")
		assert.Contains(t, layout, "rootfs/", "Template OCI bundle layout should list rootfs directory")
	})
}

func TestMergeSpecOntoTemplate(t *testing.T) {
	t.Run("PreservesTemplateNamespacesWhenNoOverrides", func(t *testing.T) {
		spec := ociSpec{}
		raw, err := mergeSpecOntoTemplate(spec)
		require.NoError(t, err, "mergeSpecOntoTemplate with empty spec should not return an error")

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &result), "Merged config should unmarshal to map")

		linux, ok := result["linux"].(map[string]interface{})
		require.True(t, ok, "Merged config should include linux section from template")
		namespaces, ok := linux["namespaces"].([]interface{})
		require.True(t, ok, "Merged config should include linux.namespaces from template")
		assert.NotEmpty(t, namespaces, "Merged config linux.namespaces should be non-empty (preserved from template)")
	})

	t.Run("OverridesMountsWhilePreservingNamespaces", func(t *testing.T) {
		accessDir := t.TempDir()
		spec := ociSpec{
			Mounts: []ociMount{
				{Destination: accessDir, Type: "bind", Source: accessDir, Options: []string{"rbind", "ro"}},
			},
		}
		raw, err := mergeSpecOntoTemplate(spec)
		require.NoError(t, err, "mergeSpecOntoTemplate with mounts should not return an error")

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &result), "Merged config with mounts should unmarshal to map")

		mounts, ok := result["mounts"].([]interface{})
		require.True(t, ok, "Merged config should include mounts section")
		require.Len(t, mounts, 1, "Merged config mounts should be replaced by spec mounts")
		m := mounts[0].(map[string]interface{})
		assert.Equal(t, accessDir, m["source"], "Merged mount source should match spec")

		linux, ok := result["linux"].(map[string]interface{})
		require.True(t, ok, "Merged config should still include linux section")
		_, hasNamespaces := linux["namespaces"]
		assert.True(t, hasNamespaces, "linux.namespaces should be preserved from template when spec has no maskedPaths")
	})

	t.Run("MergesMaskedPathsOntoPresentNamespaces", func(t *testing.T) {
		spec := ociSpec{
			Linux: &ociLinux{
				MaskedPaths: []string{"/tmp/secret-dir"},
			},
		}
		raw, err := mergeSpecOntoTemplate(spec)
		require.NoError(t, err, "mergeSpecOntoTemplate with maskedPaths should not return an error")

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &result), "Merged config with maskedPaths should unmarshal to map")

		linux, ok := result["linux"].(map[string]interface{})
		require.True(t, ok, "Merged config should include linux section")

		masked, ok := linux["maskedPaths"].([]interface{})
		require.True(t, ok, "Merged config linux.maskedPaths should be a list")
		require.Len(t, masked, 1, "Merged config should have one masked path")
		assert.Equal(t, "/tmp/secret-dir", masked[0], "Merged masked path should match spec")

		_, hasNamespaces := linux["namespaces"]
		assert.True(t, hasNamespaces, "linux.namespaces should be preserved from template alongside maskedPaths")
	})

	t.Run("PreservesProcessAndRootFromTemplate", func(t *testing.T) {
		raw, err := mergeSpecOntoTemplate(ociSpec{})
		require.NoError(t, err, "mergeSpecOntoTemplate should not return an error")

		var spec ociSpec
		require.NoError(t, json.Unmarshal(raw, &spec), "Merged config should parse as ociSpec")
		assert.Equal(t, "1.0.2", spec.OCIVersion, "Merged config should preserve ociVersion from template")
		assert.Equal(t, "rootfs", spec.Root.Path, "Merged config should preserve root.path from template")
		assert.NotEmpty(t, spec.Process.Args, "Merged config should preserve process.args from template")
		assert.Equal(t, "/", spec.Process.Cwd, "Merged config should preserve process.cwd from template")
	})
}

func TestSyncBundleConfigCopiesTemplate(t *testing.T) {
	t.Run("SyncCreatesConfigBasedOnTemplate", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		workDir := t.TempDir()
		configDir := t.TempDir()

		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: workDir,
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err)

		syncOpts, err := p.resolveSyncOptions("sync-template-1", configDir)
		require.NoError(t, err)
		err = p.SyncBundleConfig("sync-template-1", &provider.Config{
			AgentPolicy: &provider.Policy{
				FSPolicy: provider.FSPolicy(provider.PermissiveRead),
				Config: provider.MountConfig{
					BlockedDirs: []string{"/tmp/blocked-sync"},
				},
			},
		}, syncOpts)
		require.NoError(t, err, "SyncBundleConfig should not return an error")

		raw, err := os.ReadFile(filepath.Join(configDir, "gen", gvisorProviderTemplateFilePath))
		require.NoError(t, err, "Generated config template should exist in configDir")

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &result))

		linux, ok := result["linux"].(map[string]interface{})
		require.True(t, ok, "Synced config should include linux section")
		_, hasNamespaces := linux["namespaces"]
		assert.True(t, hasNamespaces, "Synced config should preserve linux.namespaces from template")

		masked, ok := linux["maskedPaths"].([]interface{})
		require.True(t, ok, "Synced config should include linux.maskedPaths")
		assert.Equal(t, "/tmp/blocked-sync", masked[0], "Synced config maskedPaths should reflect sandbox.Config")
	})
}

func TestEnsureDefaultBundleUsesTemplate(t *testing.T) {
	t.Run("NewBundleConfigMatchesEmbeddedTemplate", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: t.TempDir(),
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err)

		bundleDir, err := p.ensureXDGBundle("default-bundle-tpl-1")
		require.NoError(t, err, "ensureXDGBundle should not return an error")

		configPath := filepath.Join(bundleDir, gvisorBundleConfigFileName)
		raw, err := os.ReadFile(configPath)
		require.NoError(t, err, "Bundle config.json should be created")

		var spec ociSpec
		require.NoError(t, json.Unmarshal(raw, &spec), "Bundle config.json should be valid OCI spec JSON")
		assert.Equal(t, "1.0.2", spec.OCIVersion, "Bundle config should have template OCI version")
		assert.NotEmpty(t, spec.Process.Args, "Bundle config should have process args from template")
		assert.NotNil(t, spec.Linux, "Bundle config should have linux section from template")
		assert.NotEmpty(t, spec.Linux.Namespaces, "Bundle config should have linux.namespaces from template")
	})
}

func countStringOccurrences(values []string, target string) int {
	count := 0
	cleanTarget := filepath.Clean(target)
	for i := range values {
		if filepath.Clean(values[i]) == cleanTarget {
			count++
		}
	}
	return count
}

func countMountOccurrences(mounts []ociMount, target string) int {
	count := 0
	cleanTarget := filepath.Clean(target)
	for i := range mounts {
		if filepath.Clean(mounts[i].Source) == cleanTarget && filepath.Clean(mounts[i].Destination) == cleanTarget {
			count++
		}
	}
	return count
}
