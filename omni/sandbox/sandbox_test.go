package sandbox_test

import (
	"sync"
	"testing"

	"github.com/Shaik-Sirajuddin/omni/sandbox"
	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderFactory(t *testing.T) {
	testStore := newMemorySandboxStore("test")
	baseConfig := &sandbox.Config{
		AgentPolicy: &sandbox.Policy{
			FSPolicy: sandbox.FSPolicy(sandbox.Inherit),
			Config: sandbox.MountConfig{
				AccessDirs:  []string{"/tmp/cache"},
				BlockedDirs: []string{"/secret"},
			},
		},
	}

	t.Run("NewProvisioner", func(t *testing.T) {
		t.Log("Running provider construction validation")
		got, err := sandbox.NewProvisioner(sandbox.ProvisionerGVisor, &sandbox.Sandbox{Config: baseConfig}, sandbox.ProvisionerOptions{Store: testStore})
		require.NoError(t, err, "Creating a supported provisioner should not return an error")
		require.NotNil(t, got, "Creating a supported provisioner should return an implementation")

		_, err = sandbox.NewProvisioner(sandbox.ProvisionerKind("unknown"), nil, sandbox.ProvisionerOptions{})
		require.Error(t, err, "Creating an unsupported provisioner should return an error")
	})

	t.Run("Lifecycle", func(t *testing.T) {
		t.Log("Running seatbelt lifecycle validation")
		p, err := sandbox.NewProvisioner(sandbox.ProvisionerSeatbelt, &sandbox.Sandbox{Config: baseConfig}, sandbox.ProvisionerOptions{WorkDir: "/workspace", Store: testStore})
		require.NoError(t, err, "Creating the seatbelt provisioner should succeed")

		created, err := p.Create(sandbox.CreateSandboxParams{ID: "sandbox-1"})
		require.NoError(t, err, "Creating a sandbox should succeed")
		require.NotNil(t, created, "Creating a sandbox should return sandbox metadata")
		createdMeta := created.Sandbox()
		assert.Equal(t, "sandbox-1", createdMeta.Data.ID, "Created sandbox id should match the request")
		assert.Equal(t, "sandbox-1", createdMeta.State.PID, "Created sandbox pid should default to the sandbox id")
		assert.True(t, createdMeta.State.Active, "Created sandbox should be active")
		require.NotNil(t, createdMeta.Config, "Created sandbox should inherit the provisioner config")
		assert.Equal(t, sandbox.FSPolicy(sandbox.Inherit), createdMeta.Config.AgentPolicy.FSPolicy, "Created sandbox should inherit the agent fs policy")

		listed, err := p.List(sandbox.ListSandboxParams{Active: true})
		require.NoError(t, err, "Listing active sandboxes should succeed")
		require.Len(t, listed, 1, "Listing active sandboxes should return the created sandbox")
		assert.Subset(t, runtimeIDs(listed), []string{"sandbox-1"}, "Listing active sandboxes should include the created sandbox id")

		pid := "sandbox-1"
		got, err := p.GetSandbox(&sandbox.GetSandboxParams{PID: &pid, Active: true})
		require.NoError(t, err, "Getting an active sandbox by pid should succeed")
		assert.Equal(t, createdMeta.Data.ID, got.Sandbox().Data.ID, "Fetched sandbox should match the created sandbox")

		updatedConfig := &sandbox.Config{
			AgentPolicy: &sandbox.Policy{
				FSPolicy: sandbox.FSPolicy(sandbox.PermissiveRead),
			},
		}
		require.NoError(t, got.Sync(updatedConfig), "Syncing sandbox config should succeed")

		got, err = p.GetSandbox(&sandbox.GetSandboxParams{PID: &pid, Active: true})
		require.NoError(t, err, "Getting a sandbox after sync should succeed")
		require.NotNil(t, got.Sandbox().Config, "Synced sandbox should still have config")
		assert.Equal(t, sandbox.FSPolicy(sandbox.PermissiveRead), got.Sandbox().Config.AgentPolicy.FSPolicy, "Synced sandbox config should reflect the updated policy")

		testStore.mu.Lock()
		testStore.sandboxes = map[string]*provider.Sandbox{}
		testStore.mu.Unlock()

		got, err = p.GetSandbox(&sandbox.GetSandboxParams{PID: &pid, Active: true})
		require.NoError(t, err, "Cold get should fall back to the sandbox store")
		require.NotNil(t, got.Sandbox().Config, "Cold-loaded sandbox should still have config")
		assert.Equal(t, sandbox.FSPolicy(sandbox.PermissiveRead), got.Sandbox().Config.AgentPolicy.FSPolicy, "Cold-loaded sandbox config should come from the sandbox store")
	})

	t.Run("RuntimeCaptureAndStart", func(t *testing.T) {
		t.Log("Running deterministic gVisor runtime capture and start failure validation")
		p, err := sandbox.NewProvisioner(
			sandbox.ProvisionerGVisor,
			&sandbox.Sandbox{Config: baseConfig},
			sandbox.ProvisionerOptions{
				Store:      testStore,
				Executable: "runsc-not-installed-for-test",
			},
		)
		require.NoError(t, err, "Creating the gVisor provisioner should succeed")

		rt, err := p.Create(sandbox.CreateSandboxParams{ID: "sandbox-runtime"})
		require.Error(t, err, "Creating a gVisor runtime should fail when no OCI bundle is configured")
		assert.Nil(t, rt, "Creating a gVisor runtime without bundle should not return a runtime")
	})

	t.Run("GVisorLifecycle", func(t *testing.T) {
		t.Log("Running gVisor lifecycle validation")
		p, err := sandbox.NewProvisioner(
			sandbox.ProvisionerGVisor,
			&sandbox.Sandbox{Config: baseConfig},
			sandbox.ProvisionerOptions{
				Store:      testStore,
				Executable: "runsc-not-required-for-lifecycle-test",
			},
		)
		require.NoError(t, err, "Creating the gVisor provisioner should succeed")

		rt, err := p.Create(sandbox.CreateSandboxParams{ID: "gvisor-lifecycle-1"})
		require.Error(t, err, "Creating a gVisor runtime should fail without an OCI bundle")
		assert.Nil(t, rt, "Creating a gVisor runtime without bundle should not return a runtime instance")
	})

	t.Run("SupportedProvisioners", func(t *testing.T) {
		t.Log("Running host provisioner support validation")
		assert.Equal(t, []sandbox.ProvisionerKind{sandbox.ProvisionerGVisor}, sandbox.SupportedProvisioners("linux"), "Linux should advertise gVisor support")
		assert.Equal(t, []sandbox.ProvisionerKind{sandbox.ProvisionerSeatbelt}, sandbox.SupportedProvisioners("darwin"), "macOS should advertise seatbelt support")
		assert.Nil(t, sandbox.SupportedProvisioners("windows"), "Windows should not advertise unsupported provisioners directly")
	})
}

func runtimeIDs(items []sandbox.SandboxRuntime) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil || item.Sandbox() == nil || item.Sandbox().Data == nil {
			continue
		}
		ids = append(ids, item.Sandbox().Data.ID)
	}
	return ids
}

type memorySandboxStore struct {
	info      provider.Info
	sandboxes map[string]*provider.Sandbox
	mu        sync.Mutex
}

func newMemorySandboxStore(application string) *memorySandboxStore {
	return &memorySandboxStore{
		info:      provider.Info{Application: application},
		sandboxes: map[string]*provider.Sandbox{},
	}
}

func (s *memorySandboxStore) Info() provider.Info { return s.info }

func (s *memorySandboxStore) Create(sb *provider.Sandbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sandboxes[sb.Data.ID] = provider.CloneSandbox(sb)
	return nil
}

func (s *memorySandboxStore) Update(sb *provider.Sandbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sandboxes[sb.Data.ID] = provider.CloneSandbox(sb)
	return nil
}

func (s *memorySandboxStore) Get(params *provider.GetSandboxParams) (*provider.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sbx := range s.sandboxes {
		if params != nil {
			if params.Active && (sbx.State == nil || !sbx.State.Active) {
				continue
			}
			if params.PID != nil && (sbx.State == nil || sbx.State.PID != *params.PID) {
				continue
			}
			if params.Name != nil && (sbx.Data == nil || sbx.Data.ID != *params.Name) {
				continue
			}
		}
		return provider.CloneSandbox(sbx), nil
	}
	return nil, provider.NoProcessFound
}

func (s *memorySandboxStore) List() ([]*provider.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*provider.Sandbox, 0, len(s.sandboxes))
	for _, sbx := range s.sandboxes {
		out = append(out, provider.CloneSandbox(sbx))
	}
	return out, nil
}
