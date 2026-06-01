package bubblewrap

import (
	"testing"

	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ provider.SandboxProvisioner = (*Provisioner)(nil)
var _ provider.SandboxUpdateProvisioner = (*Provisioner)(nil)
var _ provider.SandboxDirProvisioner = (*Provisioner)(nil)
var _ provider.SandboxRuntime = (*Runtime)(nil)

func TestProvisionerBuildCommand(t *testing.T) {
	t.Run("WriteEnabledWorkspace", func(t *testing.T) {
		p, err := New(&provider.Sandbox{
			Config: &provider.Config{
				AgentPolicy: &provider.Policy{
					FSPolicy: provider.FSPolicy(provider.Inherit),
					Config: provider.MountConfig{
						AccessDirs:  []string{"/tmp/cache"},
						BlockedDirs: []string{"/secret"},
					},
				},
			},
		}, provider.ProvisionerOptions{
			Executable: "custom-bwrap",
			WorkDir:    "/workspace",
			ExtraArgs:  []string{"--unshare-net"},
			Store:      newMemoryStore("bubblewrap"),
		})
		require.NoError(t, err, "Creating the bubblewrap provisioner should not return an error")

		executable, args, err := p.BuildCommand(&provider.Sandbox{
			Config: &provider.Config{
				AgentPolicy: &provider.Policy{
					FSPolicy: provider.FSPolicy(provider.Inherit),
					Config: provider.MountConfig{
						AccessDirs:  []string{"/tmp/cache"},
						BlockedDirs: []string{"/secret"},
					},
				},
			},
		}, "sh", []string{"-lc", "pwd"})
		require.NoError(t, err, "Building a bubblewrap command should not return an error")
		assert.Equal(t, "custom-bwrap", executable, "Bubblewrap executable should respect the configured override")
		assert.Subset(t, args, []string{
			"--bind", "/workspace", "/workspace",
			"--ro-bind", "/tmp/cache", "/tmp/cache",
			"--tmpfs", "/secret",
			"--chdir", "/workspace",
			"--unshare-net",
			"--", "sh", "-lc", "pwd",
		}, "Bubblewrap args should include the expected filesystem policy and command")
	})

	t.Run("ReadOnlyWorkspace", func(t *testing.T) {
		p, err := New(&provider.Sandbox{
			Config: &provider.Config{
				AgentPolicy: &provider.Policy{
					FSPolicy: provider.FSPolicy(provider.PermissiveRead),
				},
			},
		}, provider.ProvisionerOptions{
			WorkDir: "/workspace",
			Store:   newMemoryStore("bubblewrap"),
		})
		require.NoError(t, err, "Creating the bubblewrap provisioner should not return an error")

		_, args, err := p.BuildCommand(&provider.Sandbox{
			Config: &provider.Config{
				AgentPolicy: &provider.Policy{
					FSPolicy: provider.FSPolicy(provider.PermissiveRead),
				},
			},
		}, "env", nil)
		require.NoError(t, err, "Building a bubblewrap command should not return an error")
		assert.Subset(t, args, []string{
			"--ro-bind", "/workspace", "/workspace",
			"--", "env",
		}, "Bubblewrap args should keep the workspace read-only when write access is not allowed")
	})
}

type memoryStore struct {
	info provider.Info
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
