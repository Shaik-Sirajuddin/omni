package configsync

import (
	"context"
	"testing"

	"github.com/Shaik-Sirajuddin/memory/config"
	confhooks "github.com/Shaik-Sirajuddin/memory/config/hooks"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
)

type fakeResolver struct {
	cfg      *config.OmniConfig
	watching bool
	onChange func(*config.OmniConfig)
}

func (r *fakeResolver) GetUserSettings() (*config.OmniConfig, error) {
	return r.cfg, nil
}

func (r *fakeResolver) SaveUserSettings(*config.OmniConfig) error {
	return nil
}

func (r *fakeResolver) WatchSettings(onChange func(*config.OmniConfig)) error {
	r.watching = true
	r.onChange = onChange
	return nil
}

func (r *fakeResolver) Unwatch() {
	r.watching = false
	r.onChange = nil
}

type fakeAgents struct {
	agents []Agent
}

func (r fakeAgents) ListActiveAgents(context.Context) ([]Agent, error) {
	return r.agents, nil
}

type fakeTransformer struct {
	hooks map[string]config.HookEntry
	order []string
}

func newFakeTransformer() *fakeTransformer {
	return &fakeTransformer{hooks: map[string]config.HookEntry{}}
}

func (t *fakeTransformer) Add(name string, entry config.HookEntry) bool {
	if _, ok := t.hooks[name]; ok {
		return false
	}
	t.hooks[name] = entry
	t.order = append(t.order, name)
	return true
}

func (t *fakeTransformer) GetHooks() []confhooks.Hook {
	out := make([]confhooks.Hook, 0, len(t.order))
	for _, name := range t.order {
		out = append(out, confhooks.Hook{Name: name, Entry: t.hooks[name]})
	}
	return out
}

func (t *fakeTransformer) GetHookResponse(string, any) (confhooks.HookResponseSchema, error) {
	return confhooks.HookResponseSchema{}, nil
}

func (t *fakeTransformer) GetHookResult(string, any) (confhooks.HookResultSchema, error) {
	return confhooks.HookResultSchema{}, nil
}

var _ codeagent.HookTransformer = (*fakeTransformer)(nil)

func TestRegisterAgentAppliesDefaultAndConfiguredHooks(t *testing.T) {
	customCmd := "custom-hook"
	resolver := &fakeResolver{cfg: &config.OmniConfig{
		Agent: &config.Settings{
			Hooks: map[string][]config.HookEntry{
				"PreToolUse": {{Command: &customCmd}},
			},
		},
	}}
	svc, err := NewService(ServiceOptions{
		Resolver:   resolver,
		BinaryPath: "/bin/omni",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	transformer := newFakeTransformer()
	if err := svc.RegisterAgent("agent-1", transformer); err != nil {
		t.Fatalf("RegisterAgent() error = %v", err)
	}

	if _, ok := transformer.hooks["omni.PreToolUse"]; !ok {
		t.Fatalf("missing default hook")
	}
	if _, ok := transformer.hooks["omni.config.PreToolUse.0"]; !ok {
		t.Fatalf("missing configured hook")
	}
	assertStringSlice(t, transformer.hooks["omni.PreToolUse"].Args, []string{"hook", "--event", "PreToolUse", "--agent", "agent-1"})
	assertStringSlice(t, transformer.hooks["omni.config.PreToolUse.0"].Args, []string{"--event", "PreToolUse"})
}

func TestStartRegistersActiveAgentsAndWatches(t *testing.T) {
	resolver := &fakeResolver{cfg: &config.OmniConfig{}}
	transformer := newFakeTransformer()
	svc, err := NewService(ServiceOptions{
		Resolver:      resolver,
		Agents:        fakeAgents{agents: []Agent{{ID: "agent-1", Transformer: transformer}}},
		BinaryPath:    "/bin/omni",
		WatchSettings: true,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(svc.Stop)

	_, ok := svc.Transformer("agent-1")
	if !ok {
		t.Fatalf("expected transformer to be registered")
	}
	if !resolver.watching {
		t.Fatalf("expected resolver to be watching")
	}
	if _, ok := transformer.hooks["omni.PreToolUse"]; !ok {
		t.Fatalf("missing default hook")
	}
}

func TestUnregisterAgentRemovesTransformer(t *testing.T) {
	resolver := &fakeResolver{cfg: &config.OmniConfig{}}
	svc, err := NewService(ServiceOptions{Resolver: resolver, BinaryPath: "/bin/omni"})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if err := svc.RegisterAgent("agent-1", newFakeTransformer()); err != nil {
		t.Fatalf("RegisterAgent() error = %v", err)
	}
	svc.UnregisterAgent("agent-1")

	_, ok := svc.Transformer("agent-1")
	if ok {
		t.Fatalf("expected transformer to be unregistered")
	}
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q; got %v", i, got[i], want[i], got)
		}
	}
}
