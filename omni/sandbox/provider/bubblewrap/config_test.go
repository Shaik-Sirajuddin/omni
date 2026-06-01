package bubblewrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransformerRoundTrip(t *testing.T) {
	p, err := New(nil, provider.ProvisionerOptions{
		Executable: "bwrap-custom",
		WorkDir:    "/workspace",
		ExtraArgs:  []string{"--unshare-net"},
		Store:      newMemoryStore("bubblewrap"),
	})
	require.NoError(t, err, "Creating bubblewrap provisioner should not return an error")

	cfg, err := p.TransformFromSandbox(&provider.Config{
		AgentPolicy: &provider.Policy{
			FSPolicy: provider.FSPolicy(provider.Inherit),
			Config: provider.MountConfig{
				AccessDirs:  []string{"/tmp/a", "/tmp/a", "/tmp/b"},
				BlockedDirs: []string{"/tmp/x", "/tmp/x"},
			},
		},
	})
	require.NoError(t, err, "Transforming sandbox config to bubblewrap config should not return an error")
	assert.Equal(t, "bwrap-custom", cfg.Executable, "Transformed bubblewrap config should include executable from provisioner options")
	assert.Equal(t, "/workspace", cfg.WorkDir, "Transformed bubblewrap config should include work dir from provisioner options")
	assert.Subset(t, cfg.ExtraArgs, []string{"--unshare-net"}, "Transformed bubblewrap config should include extra args from provisioner options")
	assert.True(t, cfg.Sandbox.AllowWrite, "Transformed bubblewrap config should allow write for inherit fs policy")
	assert.Equal(t, 2, len(cfg.Sandbox.AccessDirs), "Transformed bubblewrap config should deduplicate access dirs")
	assert.Equal(t, 1, len(cfg.Sandbox.BlockedDirs), "Transformed bubblewrap config should deduplicate blocked dirs")

	roundTrip, err := p.TransformToSandbox(cfg)
	require.NoError(t, err, "Transforming bubblewrap config back to sandbox config should not return an error")
	require.NotNil(t, roundTrip.AgentPolicy, "Round-trip sandbox config should include agent policy")
	assert.Equal(t, provider.FSPolicy(provider.Inherit), roundTrip.AgentPolicy.FSPolicy, "Round-trip sandbox config should keep write-enabled fs policy")
	assert.Subset(t, roundTrip.AgentPolicy.Config.AccessDirs, []string{"/tmp/a", "/tmp/b"}, "Round-trip sandbox config should include access dirs")
	assert.Subset(t, roundTrip.AgentPolicy.Config.BlockedDirs, []string{"/tmp/x"}, "Round-trip sandbox config should include blocked dirs")
}

func TestDiskStoreSaveLoad(t *testing.T) {
	store := NewStore()
	path := filepath.Join(t.TempDir(), "bubblewrap", "config.yaml")
	input := &Config{
		Executable: "bwrap",
		WorkDir:    "/workspace",
		ExtraArgs:  []string{"--unshare-net"},
		Sandbox: FilesystemConfig{
			AllowWrite:  true,
			AccessDirs:  []string{"/tmp/a", "/tmp/a"},
			BlockedDirs: []string{"/tmp/x", "/tmp/x"},
		},
	}

	err := store.Save(path, input)
	require.NoError(t, err, "Saving bubblewrap config should not return an error")

	loaded, err := store.Load(path)
	require.NoError(t, err, "Loading bubblewrap config should not return an error")
	assert.Equal(t, "bwrap", loaded.Executable, "Loaded bubblewrap config should include executable")
	assert.Equal(t, "/workspace", loaded.WorkDir, "Loaded bubblewrap config should include work dir")
	assert.Equal(t, 1, len(loaded.Sandbox.AccessDirs), "Loaded bubblewrap config should deduplicate access dirs")
	assert.Equal(t, 1, len(loaded.Sandbox.BlockedDirs), "Loaded bubblewrap config should deduplicate blocked dirs")
}

func TestProvisionerConfigMethods(t *testing.T) {
	p, err := New(nil, provider.ProvisionerOptions{
		WorkDir: t.TempDir(),
		Store:   newMemoryStore("bubblewrap"),
	})
	require.NoError(t, err, "Creating bubblewrap provisioner should not return an error")

	configPath := filepath.Join(t.TempDir(), "bubblewrap-config.yaml")
	err = p.SaveConfig(configPath, &Config{
		Executable: "bwrap",
		Sandbox: FilesystemConfig{
			AllowWrite: true,
		},
	})
	require.NoError(t, err, "Saving bubblewrap config via provisioner should not return an error")

	loaded, err := p.LoadConfig(configPath)
	require.NoError(t, err, "Loading bubblewrap config via provisioner should not return an error")
	assert.Equal(t, "bwrap", loaded.Executable, "Loaded bubblewrap config should include executable")

	sandboxCfg, err := p.TransformToSandbox(loaded)
	require.NoError(t, err, "Transforming loaded bubblewrap config to sandbox config should not return an error")
	require.NotNil(t, sandboxCfg.AgentPolicy, "Transformed sandbox config should include agent policy")
	assert.Equal(t, provider.FSPolicy(provider.Inherit), sandboxCfg.AgentPolicy.FSPolicy, "Transformed sandbox config should map allow_write to inherit fs policy")
}

func TestProvisionerUpdateAndDirs(t *testing.T) {
	workDir := t.TempDir()
	p, err := New(nil, provider.ProvisionerOptions{
		WorkDir: workDir,
		Store:   newMemoryStore("bubblewrap"),
	})
	require.NoError(t, err, "Creating bubblewrap provisioner should not return an error")

	_, err = p.Create(provider.CreateSandboxParams{
		ID: "bubblewrap-update-1",
		Config: &provider.Config{
			AgentPolicy: &provider.Policy{
				FSPolicy: provider.FSPolicy(provider.PermissiveRead),
			},
		},
	})
	require.NoError(t, err, "Creating bubblewrap runtime should not return an error")

	rt, err := p.UpdateSandbox(&provider.UpdateSandboxParams{
		ID: "bubblewrap-update-1",
		Config: &provider.Config{
			AgentPolicy: &provider.Policy{
				FSPolicy: provider.FSPolicy(provider.Inherit),
			},
		},
	})
	require.NoError(t, err, "Updating bubblewrap runtime should not return an error")
	require.NotNil(t, rt, "Updating bubblewrap runtime should return runtime")

	err = p.CreateDir("nested/one")
	require.NoError(t, err, "Creating relative directory through provisioner should not return an error")
	dirs, err := p.ListDirs("nested")
	require.NoError(t, err, "Listing relative directories through provisioner should not return an error")
	assert.Subset(t, dirs, []string{"one"}, "Directory listing should include created directory")

	absolute := filepath.Join(workDir, "abs-dir")
	err = p.CreateDir(absolute)
	require.NoError(t, err, "Creating absolute directory through provisioner should not return an error")
	info, err := os.Stat(absolute)
	require.NoError(t, err, "Stating created absolute directory should not return an error")
	assert.True(t, info.IsDir(), "Created absolute path should be a directory")
}
