package gvisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvisionerExecCommand(t *testing.T) {
	t.Run("RunscExecShape", func(t *testing.T) {
		previous := runscNeedsIgnoreCgroupsFn
		runscNeedsIgnoreCgroupsFn = func() bool { return false }
		t.Cleanup(func() { runscNeedsIgnoreCgroupsFn = previous })

		p, err := New(nil, provider.ProvisionerOptions{
			Executable: "custom-runsc",
			GlobalArgs: []string{"--root=/run/user/1000/runsc"},
			ExtraArgs:  []string{"--stdio=none"},
			Store:      newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating the gVisor provisioner should not return an error")

		executable, args, err := p.ExecCommand(&provider.Sandbox{
			State: &provider.State{PID: "sandbox-1"},
		}, "/bin/sh", []string{"-lc", "pwd"})
		require.NoError(t, err, "Building a gVisor exec command should not return an error")
		assert.Equal(t, "custom-runsc", executable, "gVisor executable should respect the configured override")
		assert.Equal(t, []string{
			"--root=/run/user/1000/runsc",
			"exec",
			"sandbox-1",
			"/bin/sh",
			"-lc",
			"pwd",
			"--stdio=none",
		}, args, "gVisor exec args should match the expected runsc invocation")
	})

	t.Run("Validation", func(t *testing.T) {
		previous := runscNeedsIgnoreCgroupsFn
		runscNeedsIgnoreCgroupsFn = func() bool { return false }
		t.Cleanup(func() { runscNeedsIgnoreCgroupsFn = previous })

		p, err := New(nil, provider.ProvisionerOptions{Store: newMemoryStore("gvisor")})
		require.NoError(t, err, "Creating the gVisor provisioner should not return an error")

		_, _, err = p.ExecCommand(&provider.Sandbox{}, "/bin/sh", nil)
		require.Error(t, err, "Building a gVisor command without a pid should return an error")

		_, _, err = p.ExecCommand(&provider.Sandbox{State: &provider.State{PID: "sandbox-1"}}, "", nil)
		require.Error(t, err, "Building a gVisor command without a command should return an error")
	})

	t.Run("AppliesRuntimeRootWhenProvided", func(t *testing.T) {
		previous := runscNeedsIgnoreCgroupsFn
		runscNeedsIgnoreCgroupsFn = func() bool { return false }
		t.Cleanup(func() { runscNeedsIgnoreCgroupsFn = previous })

		p, err := New(nil, provider.ProvisionerOptions{
			RuntimeRoot: "/tmp/runsc-custom-root",
			Store:       newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		_, args, err := p.ExecCommand(&provider.Sandbox{
			State: &provider.State{PID: "sandbox-2"},
		}, "/bin/sh", []string{"-lc", "pwd"})
		require.NoError(t, err, "Building a gVisor command with RuntimeRoot should not return an error")
		assert.Equal(t, []string{
			"--root",
			"/tmp/runsc-custom-root",
			"exec",
			"sandbox-2",
			"/bin/sh",
			"-lc",
			"pwd",
		}, args, "gVisor exec args should include explicit RuntimeRoot before exec")
	})

	t.Run("AddsIgnoreCgroupsWhenNeeded", func(t *testing.T) {
		previous := runscNeedsIgnoreCgroupsFn
		runscNeedsIgnoreCgroupsFn = func() bool { return true }
		t.Cleanup(func() { runscNeedsIgnoreCgroupsFn = previous })

		p, err := New(nil, provider.ProvisionerOptions{
			RuntimeRoot: "/tmp/runsc-custom-root",
			Store:       newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		_, args, err := p.ExecCommand(&provider.Sandbox{
			State: &provider.State{PID: "sandbox-3"},
		}, "/bin/sh", []string{"-lc", "pwd"})
		require.NoError(t, err, "Building a gVisor command should not return an error")
		assert.Equal(t, []string{
			"--root",
			"/tmp/runsc-custom-root",
			"-ignore-cgroups",
			"exec",
			"sandbox-3",
			"/bin/sh",
			"-lc",
			"pwd",
		}, args, "gVisor exec args should include ignore-cgroups when cgroups are unavailable")
	})
}

func TestParseRuntimeList(t *testing.T) {
	t.Run("HeaderAndRows", func(t *testing.T) {
		out := `ID               PID         STATUS      BUNDLE      CREATED                          OWNER
sandbox-1        1234        running     /tmp/b1     2026-04-18T12:00:00.000000+00:00  user
sandbox-2        0           stopped     /tmp/b2     2026-04-18T12:01:00.000000+00:00  user`
		items := parseRuntimeList(out)
		require.Len(t, items, 2, "Parser should return each non-header runtime row")
		assert.Equal(t, "sandbox-1", items[0].ID, "Parser should capture runtime id from the first column")
		assert.Equal(t, "1234", items[0].PID, "Parser should capture runtime pid from the second column")
		assert.Equal(t, "running", items[0].Status, "Parser should capture normalized runtime status from the third column")
		assert.Equal(t, "sandbox-2", items[1].ID, "Parser should capture second runtime id")
		assert.Equal(t, "stopped", items[1].Status, "Parser should capture stopped runtime status")
	})

	t.Run("EmptyInput", func(t *testing.T) {
		items := parseRuntimeList("")
		assert.Empty(t, items, "Parser should return no runtimes for empty output")
	})
}

func TestRuntimeActive(t *testing.T) {
	assert.True(t, runtimeActive("running"), "Running status should be considered active")
	assert.True(t, runtimeActive("created"), "Created status should be considered active")
	assert.True(t, runtimeActive("paused"), "Paused status should be considered active")
	assert.False(t, runtimeActive("stopped"), "Stopped status should not be considered active")
	assert.False(t, runtimeActive(""), "Empty status should not be considered active")
}

func TestProvisionerDirectoryOps(t *testing.T) {
	t.Run("CreateAndListRelativeDirs", func(t *testing.T) {
		workDir := t.TempDir()
		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: workDir,
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		require.NoError(t, p.CreateDir("alpha"), "CreateDir should create relative directory under WorkDir")
		require.NoError(t, p.CreateDir("beta"), "CreateDir should create a second directory under WorkDir")
		require.NoError(t, os.WriteFile(filepath.Join(workDir, "note.txt"), []byte("x"), 0o644), "Test setup should create a non-directory file")

		dirs, err := p.ListDirs(".")
		require.NoError(t, err, "ListDirs should list directories under relative path")
		assert.Subset(t, dirs, []string{"alpha", "beta"}, "ListDirs should include created directories")
		assert.NotContains(t, dirs, "note.txt", "ListDirs should not include files")
	})
}

func TestUpdateSandbox(t *testing.T) {
	t.Run("Validation", func(t *testing.T) {
		p, err := New(nil, provider.ProvisionerOptions{WorkDir: t.TempDir()})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		_, err = p.UpdateSandbox(nil)
		require.Error(t, err, "UpdateSandbox should return an error when params are missing")

		_, err = p.UpdateSandbox(&provider.UpdateSandboxParams{})
		require.Error(t, err, "UpdateSandbox should return an error when sandbox id is missing")
	})

	t.Run("UpdateExistingSandbox", func(t *testing.T) {
		p, err := New(nil, provider.ProvisionerOptions{WorkDir: t.TempDir()})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		_, err = p.state.Create(provider.ProvisionerGVisor, provider.CreateSandboxParams{
			ID: "sandbox-update-1",
			Config: &provider.Config{
				AgentPolicy: &provider.Policy{FSPolicy: provider.FSPolicy(provider.Inherit)},
			},
		}, nil)
		require.NoError(t, err, "Test setup should create sandbox metadata in provisioner state")

		rt, err := p.UpdateSandbox(&provider.UpdateSandboxParams{
			ID: "sandbox-update-1",
			Config: &provider.Config{
				AgentPolicy: &provider.Policy{FSPolicy: provider.FSPolicy(provider.PermissiveRead)},
			},
		})
		require.NoError(t, err, "UpdateSandbox should update config for existing sandbox id")
		require.NotNil(t, rt, "UpdateSandbox should return runtime for existing sandbox")
		require.NotNil(t, rt.Sandbox().Config, "Updated runtime should still include config")
		assert.Equal(t, provider.FSPolicy(provider.PermissiveRead), rt.Sandbox().Config.AgentPolicy.FSPolicy, "Updated runtime config should reflect requested policy")
	})
}

func TestResolveBundlePath(t *testing.T) {
	t.Run("UsesConfiguredBundle", func(t *testing.T) {
		workDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(workDir, "config.json"), []byte("{}"), 0o644), "Test setup should write config.json in WorkDir")
		p, err := New(nil, provider.ProvisionerOptions{WorkDir: workDir})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		bundle, err := p.resolveBundle("sandbox-bundle-1", "")
		require.NoError(t, err, "resolveBundle should return configured WorkDir bundle when config exists")
		assert.Equal(t, filepath.Clean(workDir), bundle, "resolveBundle should use configured WorkDir bundle path")
	})

	t.Run("CreatesDefaultBundleWhenWorkDirIsNotBundle", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		p, err := New(nil, provider.ProvisionerOptions{WorkDir: t.TempDir()})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		id := fmt.Sprintf("sandbox-bundle-%d", time.Now().UnixNano())
		bundle, err := p.resolveBundle(id, "")
		require.NoError(t, err, "resolveBundle should create default bundle when WorkDir is not an OCI bundle")
		require.FileExists(t, filepath.Join(bundle, "config.json"), "resolveBundlePath should create config.json in default bundle")

		raw, err := os.ReadFile(filepath.Join(bundle, "config.json"))
		require.NoError(t, err, "Test should read generated default config.json")
		var spec ociSpec
		require.NoError(t, json.Unmarshal(raw, &spec), "Generated config.json should be valid OCI JSON")
		assert.Equal(t, "1.0.2", spec.OCIVersion, "Generated default OCI spec should set ociVersion")
		assert.Equal(t, []string{"/bin/sh", "-c", "sleep infinity"}, spec.Process.Args, "Generated default OCI spec should keep sandbox process alive")
		assert.Equal(t, "rootfs", spec.Root.Path, "Generated default OCI spec should set a relative rootfs path")
		require.DirExists(t, filepath.Join(bundle, "rootfs"), "resolveBundlePath should ensure default rootfs directory exists")
	})
}

func TestResolveRunscRoot(t *testing.T) {
	t.Run("CreatesDefaultRootUnderXDGDataHome", func(t *testing.T) {
		xdgHome := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdgHome)

		p, err := New(nil, provider.ProvisionerOptions{Store: newMemoryStore("gvisor")})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		root, err := p.resolveRunscRoot()
		require.NoError(t, err, "resolveRunscRoot should create default runsc root path")
		require.DirExists(t, root, "Default runsc root should exist on disk")
		assert.Contains(t, filepath.Clean(root), filepath.Join("memory", "sandboxes", "gvisor", "runsc-root"), "Default runsc root should resolve in gVisor runtime root folder")
	})

	t.Run("UsesProvidedRuntimeRoot", func(t *testing.T) {
		rootDir := filepath.Join(t.TempDir(), "runsc-root")
		p, err := New(nil, provider.ProvisionerOptions{
			RuntimeRoot: rootDir,
			Store:       newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		root, err := p.resolveRunscRoot()
		require.NoError(t, err, "resolveRunscRoot should use provided RuntimeRoot")
		assert.Equal(t, rootDir, root, "resolveRunscRoot should return provided RuntimeRoot")
		require.DirExists(t, rootDir, "resolveRunscRoot should ensure provided RuntimeRoot directory exists")
	})
}

func TestSpecFromConfig(t *testing.T) {
	t.Run("IncludesSystemBinariesWorkspaceAndPolicyMappings", func(t *testing.T) {
		workDir := t.TempDir()
		accessDir := t.TempDir()
		blockedDir := "/tmp/blocked-config-dir"

		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: workDir,
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		spec, err := p.SpecFromConfig(&provider.Config{
			AgentPolicy: &provider.Policy{
				FSPolicy: provider.FSPolicy(provider.PermissiveRead),
				Config: provider.MountConfig{
					AccessDirs:  []string{accessDir},
					BlockedDirs: []string{blockedDir},
				},
			},
		}, workDir)
		require.NoError(t, err, "SpecFromConfig should convert sandbox config into OCI spec")
		assert.Equal(t, "1.0.2", spec.OCIVersion, "OCI version should be set on generated spec")

		require.True(t, hasMount(spec.Mounts, "/usr/bin", "/usr/bin"), "Generated spec should include /usr/bin mount by default")
		require.True(t, hasMount(spec.Mounts, workDir, workDir), "Generated spec should include workspace read-write mount by default")
		require.True(t, hasMount(spec.Mounts, accessDir, accessDir), "Generated spec should include access-dir mount mapping")
		require.NotNil(t, spec.Linux, "Generated spec should include linux section when blocked directories exist")
		assert.Subset(t, spec.Linux.MaskedPaths, []string{blockedDir}, "Generated spec should map blocked dirs to maskedPaths")
	})
}

func TestSyncBundleConfig(t *testing.T) {
	t.Run("WritesManagedDefaultBundleConfig", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		workDir := t.TempDir()
		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: workDir,
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		syncOpts, err := p.resolveSyncOptions("sandbox-sync-config-1", "")
		require.NoError(t, err, "resolveSyncOptions should not return an error")
		err = p.SyncBundleConfig("sandbox-sync-config-1", &provider.Config{
			AgentPolicy: &provider.Policy{
				FSPolicy: provider.FSPolicy(provider.PermissiveRead),
				Config: provider.MountConfig{
					BlockedDirs: []string{"/tmp/blocked-sync-dir"},
				},
			},
		}, syncOpts)
		require.NoError(t, err, "SyncBundleConfig should write generated config for managed bundle")

		bundle, err := p.resolveBundle("sandbox-sync-config-1", "")
		require.NoError(t, err, "resolveBundle should resolve managed bundle path")

		raw, err := os.ReadFile(filepath.Join(bundle, "config.json"))
		require.NoError(t, err, "Test should read synced bundle config")

		var spec ociSpec
		require.NoError(t, json.Unmarshal(raw, &spec), "Synced bundle config should parse as OCI spec")
		require.True(t, hasMount(spec.Mounts, workDir, workDir), "Synced managed bundle should include workspace mount mapping")
		require.NotNil(t, spec.Linux, "Synced managed bundle should include linux section for blocked dirs")
		assert.Subset(t, spec.Linux.MaskedPaths, []string{"/tmp/blocked-sync-dir"}, "Synced managed bundle should map blocked dirs to maskedPaths")
	})

	t.Run("SkipsUserManagedBundleConfig", func(t *testing.T) {
		workDir := t.TempDir()
		original := "{\n  \"ociVersion\": \"1.0.2\"\n}\n"
		require.NoError(t, os.WriteFile(filepath.Join(workDir, "config.json"), []byte(original), 0o644), "Test setup should create caller-managed config.json")

		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir: workDir,
			Store:   newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		syncOpts2, err := p.resolveSyncOptions("sandbox-sync-config-2", "")
		require.NoError(t, err, "resolveSyncOptions should not return an error for user-managed bundle")
		require.NoError(t, p.SyncBundleConfig("sandbox-sync-config-2", &provider.Config{}, syncOpts2), "SyncBundleConfig should not fail for user-managed bundle path")

		raw, err := os.ReadFile(filepath.Join(workDir, "config.json"))
		require.NoError(t, err, "Test should read caller-managed config.json after sync")
		assert.Equal(t, original, string(raw), "SyncBundleConfig should not overwrite caller-managed bundle config")
	})
}

func TestCreateConfigDirTemplates(t *testing.T) {
	t.Run("WritesCommonAndProviderTemplatesBeforeRuntimeCreate", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		configDir := t.TempDir()
		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir:      t.TempDir(),
			Executable:   "runsc-not-installed-for-template-test",
			ConfigParser: jsonFileConfigParser{},
			Store:        newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		_, err = p.Create(provider.CreateSandboxParams{
			ID:        "template-create-1",
			ConfigDir: configDir,
		})
		require.Error(t, err, "Create should fail later when runsc executable is unavailable in test")

		require.FileExists(t, filepath.Join(configDir, "config.json"), "Create should provision common sandbox config template file")
		require.FileExists(t, filepath.Join(configDir, "gen", "config.json"), "Create should provision gVisor-specific template file")
	})
}

func TestRuntimeSyncConfigDir(t *testing.T) {
	t.Run("PersistsCommonConfigAndProviderTemplate", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		workDir := t.TempDir()
		configDir := t.TempDir()
		p, err := New(nil, provider.ProvisionerOptions{
			WorkDir:      workDir,
			ConfigParser: jsonFileConfigParser{},
			Store:        newMemoryStore("gvisor"),
		})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		sbx, err := p.state.Create(provider.ProvisionerGVisor, provider.CreateSandboxParams{
			ID:        "sync-config-dir-1",
			ConfigDir: configDir,
			Config: &provider.Config{
				AgentPolicy: &provider.Policy{FSPolicy: provider.FSPolicy(provider.PermissiveRead)},
			},
		}, nil)
		require.NoError(t, err, "Test setup should create sandbox state entry")
		rt := p.wrap(sbx)

		err = rt.Sync(&provider.Config{
			AgentPolicy: &provider.Policy{FSPolicy: provider.FSPolicy(provider.Inherit)},
		})
		require.NoError(t, err, "Runtime Sync should persist common config and refresh generated templates")

		raw, err := os.ReadFile(filepath.Join(configDir, "config.json"))
		require.NoError(t, err, "Test should read synced common config")
		var cfg provider.Config
		require.NoError(t, json.Unmarshal(raw, &cfg), "Synced common config should be valid JSON")
		require.NotNil(t, cfg.AgentPolicy, "Synced common config should include agent policy")
		assert.Equal(t, provider.FSPolicy(provider.Inherit), cfg.AgentPolicy.FSPolicy, "Synced common config should store updated policy")

		require.FileExists(t, filepath.Join(configDir, "gen", "config.json"), "Sync should write provider template file in gen directory")
	})
}

func TestRootlessUIDMapHelpers(t *testing.T) {
	t.Run("EnsureSubIDEntry", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "subuid")
		content := strings.Join([]string{
			"# sample",
			"alice:100000:65536",
			"",
		}, "\n")
		require.NoError(t, os.WriteFile(file, []byte(content), 0o644), "Writing subuid test fixture should not return an error")

		require.NoError(t, ensureSubIDEntry(file, []string{"alice"}), "Subid entry validation should pass when username exists in file")
		require.NoError(t, ensureSubIDEntry(file, []string{"1000", "alice"}), "Subid entry validation should pass when any candidate exists in file")

		err := ensureSubIDEntry(file, []string{"bob"})
		require.Error(t, err, "Subid entry validation should fail when user is missing in file")
		assert.Contains(t, err.Error(), "missing entry", "Subid entry validation error should include missing entry guidance")
	})

	t.Run("EnsureSetuidBinary", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "newuidmap")
		require.NoError(t, os.WriteFile(file, []byte("#!/bin/sh\n"), 0o755), "Writing binary fixture should not return an error")

		err := ensureSetuidBinary(file, "newuidmap")
		require.Error(t, err, "Setuid validation should fail when setuid bit is missing")
		assert.Contains(t, err.Error(), "must have setuid bit", "Setuid validation error should explain required setuid bit")

		require.NoError(t, os.Chmod(file, 0o4755), "Setting setuid bit on binary fixture should not return an error")
		info, statErr := os.Stat(file)
		require.NoError(t, statErr, "Stating binary fixture after chmod should not return an error")
		if info.Mode()&os.ModeSetuid == 0 {
			t.Skip("Filesystem should preserve setuid bit for this validation scenario")
		}
		err = ensureSetuidBinary(file, "newuidmap")
		require.Error(t, err, "Setuid validation should fail when binary is not root-owned")
		assert.Contains(t, err.Error(), "must be owned by root", "Setuid validation should explain root ownership requirement")
	})

	t.Run("UserNameCandidates", func(t *testing.T) {
		candidates := userNameCandidates("domain\\alice", "1000")
		assert.Subset(t, candidates, []string{"domain\\alice", "alice", "1000"}, "Username candidates should include raw username, short name, and uid")
	})

	t.Run("WrapRunscCreateError", func(t *testing.T) {
		p, err := New(nil, provider.ProvisionerOptions{Store: newMemoryStore("gvisor")})
		require.NoError(t, err, "Creating gVisor provisioner should not return an error")

		createErr := fmt.Errorf("creating container: cannot create gofer process: newuidmap failed: exit status 1")
		wrapped := p.wrapRunscCreateError("sandbox-map-1", createErr)
		require.Error(t, wrapped, "Wrapping runsc create errors should return an error")
		assert.Contains(t, wrapped.Error(), "rootless user namespace mapping failed", "Wrapped newuidmap error should include rootless mapping guidance")
		assert.Contains(t, wrapped.Error(), "/etc/subuid", "Wrapped newuidmap error should include subordinate id mapping guidance")

		cgroupErr := fmt.Errorf("creating container: cannot set up cgroup for root: configuring cgroup: open /sys/fs/cgroup/cgroup.subtree_control: permission denied")
		cgroupWrapped := p.wrapRunscCreateError("sandbox-cgroup-1", cgroupErr)
		require.Error(t, cgroupWrapped, "Wrapping runsc cgroup errors should return an error")
		assert.Contains(t, cgroupWrapped.Error(), "cgroup delegation is unavailable", "Wrapped cgroup error should include delegation guidance")
		assert.Contains(t, cgroupWrapped.Error(), "-ignore-cgroups", "Wrapped cgroup error should include ignore-cgroups guidance")
	})
}

func hasMount(mounts []ociMount, source string, destination string) bool {
	source = filepath.Clean(source)
	destination = filepath.Clean(destination)
	for i := range mounts {
		if filepath.Clean(mounts[i].Source) == source && filepath.Clean(mounts[i].Destination) == destination {
			return true
		}
	}
	return false
}

type memoryStore struct {
	info provider.Info
}

type jsonFileConfigParser struct{}

func (jsonFileConfigParser) Load(filePath string) (*provider.Config, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return &provider.Config{}, nil
	}
	var cfg provider.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (jsonFileConfigParser) Validate(config *provider.Config) error {
	if config == nil {
		return fmt.Errorf("config is required")
	}
	return nil
}

func (jsonFileConfigParser) Save(config *provider.Config, filePath string) error {
	if config == nil {
		return fmt.Errorf("config is required")
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filePath, raw, 0o644)
}

func newMemoryStore(application string) *memoryStore {
	return &memoryStore{info: provider.Info{Application: application}}
}

func (s *memoryStore) Info() provider.Info            { return s.info }
func (s *memoryStore) Create(*provider.Sandbox) error { return nil }
func (s *memoryStore) Update(*provider.Sandbox) error { return nil }
func (s *memoryStore) Get(*provider.GetSandboxParams) (*provider.Sandbox, error) {
	return nil, provider.NoProcessFound
}
func (s *memoryStore) List() ([]*provider.Sandbox, error) { return nil, nil }
