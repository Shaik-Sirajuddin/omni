package impl

import (
	"strings"
	"testing"
)

func TestLocalDoctorTerminals_HasEntries(t *testing.T) {
	result, err := DoctorTerminalsStandalone()
	if err != nil {
		t.Fatalf("DoctorTerminalsStandalone() returned error: %v", err)
	}
	if result == nil {
		t.Fatal("DoctorTerminalsStandalone() returned nil result")
	}
	if len(result.Terminals) == 0 {
		t.Fatal("expected at least one terminal entry (zellij), got none")
	}
}

func TestLocalDoctorTerminals_ZellijPresent(t *testing.T) {
	result, err := DoctorTerminalsStandalone()
	if err != nil {
		t.Fatalf("DoctorTerminalsStandalone() error: %v", err)
	}
	found := false
	for _, s := range result.Terminals {
		if s.Name == "zellij" {
			found = true
			// Installed is a bool — just accessing it to confirm no nil-panic
			_ = s.Installed
			break
		}
	}
	if !found {
		t.Error("expected zellij entry in DoctorTerminalsStandalone result")
	}
}

func TestLocalInstallTerminal_UnknownName(t *testing.T) {
	err := InstallTerminalStandalone("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown terminal name, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("expected error to contain the terminal name, got: %v", err)
	}
}

func TestLocalInstallTerminal_KnownName(t *testing.T) {
	// zellij may or may not be installed; either outcome is acceptable.
	// The important property: no nil-panic and no "operator is required" error.
	err := InstallTerminalStandalone("zellij")
	if err != nil && strings.Contains(err.Error(), "operator is required") {
		t.Errorf("unexpected 'operator is required' error: %v", err)
	}
}
