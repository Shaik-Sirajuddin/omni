package zellij

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/terminal"
)

var ErrNotInstalled = errors.New("zellij: not installed")
var ErrSessionExists = errors.New("zellij: session already exists")
var ErrSessionNotFound = errors.New("zellij: session not found")

type ZellijTerminal struct {
	transformer *ZellijTransformer
}

func New() *ZellijTerminal {
	return &ZellijTerminal{transformer: &ZellijTransformer{}}
}

func (z *ZellijTerminal) Transformer() terminal.Transformer {
	return z.transformer
}

func (z *ZellijTerminal) CheckInstallation() error {
	_, err := exec.LookPath("zellij")
	if err != nil {
		return ErrNotInstalled
	}
	return nil
}

func (z *ZellijTerminal) Install() error {
	asset, err := zellijAsset()
	if err != nil {
		return err
	}
	installDir := os.ExpandEnv("$HOME/.local/bin")
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return fmt.Errorf("zellij install: mkdir %s: %w", installDir, err)
	}
	url := "https://github.com/zellij-org/zellij/releases/latest/download/" + asset
	cmd := exec.Command("bash", "-c",
		fmt.Sprintf(`curl -L %s | tar -xz -C %s`, url, installDir),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func zellijAsset() (string, error) {
	var osTag string
	switch runtime.GOOS {
	case "linux":
		osTag = "unknown-linux-musl"
	case "darwin":
		osTag = "apple-darwin"
	default:
		return "", fmt.Errorf("zellij install: unsupported OS %q — install manually from https://github.com/zellij-org/zellij/releases", runtime.GOOS)
	}
	var archTag string
	switch runtime.GOARCH {
	case "amd64":
		archTag = "x86_64"
	case "arm64":
		archTag = "aarch64"
	default:
		return "", fmt.Errorf("zellij install: unsupported arch %q — install manually from https://github.com/zellij-org/zellij/releases", runtime.GOARCH)
	}
	return fmt.Sprintf("zellij-%s-%s.tar.gz", archTag, osTag), nil
}

func (z *ZellijTerminal) Templates() []terminal.Template {
	return []terminal.Template{
		{
			Name: "single",
			Layout: terminal.Layout{
				Tabs: []terminal.TabLayout{
					{Name: "main", Panes: []terminal.PaneLayout{{StartSuspended: true}}},
				},
			},
		},
		{
			Name: "dev",
			Layout: terminal.Layout{
				Tabs: []terminal.TabLayout{
					{Name: "editor", Panes: []terminal.PaneLayout{{StartSuspended: true}}},
					{Name: "shell", Panes: []terminal.PaneLayout{{StartSuspended: true}}},
					{Name: "logs", Panes: []terminal.PaneLayout{{StartSuspended: true}}},
				},
			},
		},
	}
}

func (z *ZellijTerminal) InitializeSession(params terminal.SessionParams) error {
	if params.Name == "" {
		return errors.New("zellij: session name required")
	}

	exists, err := z.CheckSessionExists(params.Name)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: %s", ErrSessionExists, params.Name)
	}

	kdl, err := z.transformer.ToNativeWithFocus(params.Layout, params.FocusedTab)
	if err != nil {
		return fmt.Errorf("zellij: layout render: %w", err)
	}

	tmp, err := os.CreateTemp("", "zellij-layout-*.kdl")
	if err != nil {
		return fmt.Errorf("zellij: tmp layout file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(kdl); err != nil {
		tmp.Close()
		return fmt.Errorf("zellij: write layout: %w", err)
	}
	tmp.Close()

	cmd := exec.Command("zellij", "--session", params.Name, "--layout", tmp.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (z *ZellijTerminal) ListSessions() ([]terminal.Session, error) {
	out, err := exec.Command("zellij", "list-sessions", "--short").Output()
	if err != nil {
		// No sessions returns non-zero on some versions
		return nil, nil
	}
	var sessions []terminal.Session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			sessions = append(sessions, terminal.Session{Name: name})
		}
	}
	return sessions, nil
}

func (z *ZellijTerminal) CheckSessionExists(name string) (bool, error) {
	sessions, err := z.ListSessions()
	if err != nil {
		return false, err
	}
	for _, s := range sessions {
		if s.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (z *ZellijTerminal) GetSession(name string) (*terminal.Session, error) {
	sessions, err := z.ListSessions()
	if err != nil {
		return nil, err
	}
	for _, s := range sessions {
		if s.Name == name {
			s := s
			return &s, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, name)
}
