package configsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
)

type fakeSettingsResolver struct {
	user      *codeagent.Settings
	saved     *codeagent.Settings
	watch     func(*codeagent.Settings)
	stopCount int
}

func (r *fakeSettingsResolver) GetUserSettings() (*codeagent.Settings, error) {
	return r.user, nil
}

func (r *fakeSettingsResolver) GetWorkspaceSettings(dir sandbox.WorkspaceDir) (*codeagent.Settings, error) {
	return readTestAgySettings(filepath.Join(string(dir), ".agy", "settings.json"))
}

func (r *fakeSettingsResolver) SaveDefaultSettings(s *codeagent.Settings) error {
	r.saved = s
	r.user = s
	return nil
}

func (r *fakeSettingsResolver) WatchDefaultSettings(fn func(*codeagent.Settings)) error {
	r.watch = fn
	return nil
}

func (r *fakeSettingsResolver) StopWatch() {
	r.stopCount++
}

func TestRegisterSettingsTargetSyncsAgyDefaultToWorkspace(t *testing.T) {
	workDir := t.TempDir()
	resolver := &fakeSettingsResolver{user: &codeagent.Settings{
		Provider: ProviderAgy,
		Config: codeagent.Config{
			Model:          codeagent.Model{Provider: ProviderAgy, Model: "agy-model"},
			PermissionMode: codeagent.PermissionAcceptEdits,
		},
	}}
	svc, err := NewService(ServiceOptions{
		Resolver:   &fakeResolver{cfg: nil},
		BinaryPath: "/bin/omni",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = svc.RegisterSettingsTarget(SettingsSyncTarget{
		AgentID:      "agy-1",
		Provider:     ProviderAgy,
		Resolver:     resolver,
		WorkspaceDir: workDir,
	})
	if err != nil {
		t.Fatalf("RegisterSettingsTarget() error = %v", err)
	}

	got := readRawJSON(t, filepath.Join(workDir, ".agy", "settings.json"))
	if string(got["model"]) != `"agy-model"` {
		t.Fatalf("model = %s, want agy-model", got["model"])
	}
	var permissions struct {
		DefaultMode string `json:"defaultMode"`
	}
	if err := json.Unmarshal(got["permissions"], &permissions); err != nil {
		t.Fatalf("decode permissions: %v", err)
	}
	if permissions.DefaultMode != "acceptEdits" {
		t.Fatalf("permissions.defaultMode = %q, want acceptEdits", permissions.DefaultMode)
	}
}

func TestAgyWorkspaceWatcherSyncsWorkspaceToDefault(t *testing.T) {
	workDir := t.TempDir()
	resolver := &fakeSettingsResolver{user: &codeagent.Settings{
		Provider: ProviderAgy,
		Config: codeagent.Config{
			Model:          codeagent.Model{Provider: ProviderAgy, Model: "old-model"},
			PermissionMode: codeagent.PermissionDefault,
		},
	}}
	svc, err := NewService(ServiceOptions{
		Resolver:   &fakeResolver{cfg: nil},
		BinaryPath: "/bin/omni",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = svc.RegisterSettingsTarget(SettingsSyncTarget{
		AgentID:      "agy-1",
		Provider:     ProviderAgy,
		Resolver:     resolver,
		WorkspaceDir: workDir,
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RegisterSettingsTarget() error = %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(svc.Stop)

	writeJSON(t, filepath.Join(workDir, ".agy", "settings.json"), map[string]any{
		"model":       "new-model",
		"permissions": map[string]any{"defaultMode": "plan"},
	})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if resolver.saved != nil &&
			resolver.saved.Config.Model.Model == "new-model" &&
			resolver.saved.Config.PermissionMode == codeagent.PermissionPlan {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("workspace change was not synced to default; saved = %#v", resolver.saved)
}

func readTestAgySettings(path string) (*codeagent.Settings, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &codeagent.Settings{Provider: ProviderAgy}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		Model       string `json:"model"`
		Permissions struct {
			DefaultMode string `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &codeagent.Settings{
		Provider: ProviderAgy,
		Config: codeagent.Config{
			Model:          codeagent.Model{Provider: ProviderAgy, Model: raw.Model},
			PermissionMode: codeagent.PermissionMode(raw.Permissions.DefaultMode),
		},
	}, nil
}

func readRawJSON(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return raw
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
