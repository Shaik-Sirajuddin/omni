//go:build windows

// omni-installer registers omni-server as a Windows Service and sets up
// the required AppData directories. Run as Administrator.
//
// Usage:
//
//	omni-installer.exe           — install and start service
//	omni-installer.exe --uninstall — stop and remove service
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName = "omni"
	serviceDesc = "Omni agent orchestration daemon"
)

func main() {
	uninstall := flag.Bool("uninstall", false, "stop and remove the omni service")
	flag.Parse()

	var err error
	if *uninstall {
		err = removeService()
	} else {
		err = installService()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func installService() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve installer path: %w", err)
	}
	// omni-server.exe sits alongside the installer
	serverExe := filepath.Join(filepath.Dir(exePath), "omni-server.exe")
	if _, err := os.Stat(serverExe); err != nil {
		return fmt.Errorf("omni-server.exe not found at %s: %w", serverExe, err)
	}

	if err := createAppDataDirs(); err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (run as Administrator?): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %q already exists — run --uninstall first", serviceName)
	}

	s, err = m.CreateService(serviceName, serverExe, mgr.Config{
		StartType:        mgr.StartAutomatic,
		DisplayName:      "Omni Daemon",
		Description:      serviceDesc,
		DelayedAutoStart: true,
	})
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		// non-fatal — event log is optional
		fmt.Fprintf(os.Stderr, "warn: eventlog setup: %v\n", err)
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}

	fmt.Printf("Service %q installed and started.\n", serviceName)
	fmt.Printf("Manage with: sc query %s\n", serviceName)
	return nil
}

func removeService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q not found: %w", serviceName, err)
	}
	defer s.Close()

	// stop first, ignore "not running" error
	_, _ = s.Control(svcStop)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}

	_ = eventlog.Remove(serviceName)
	fmt.Printf("Service %q removed.\n", serviceName)
	return nil
}

func createAppDataDirs() error {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return errors.New("LOCALAPPDATA not set")
	}
	dirs := []string{
		filepath.Join(localAppData, "omni"),
		filepath.Join(localAppData, "omni", "run"), // socket dir
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	configPath := filepath.Join(localAppData, "omni", "config.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte("# omni configuration\n"), 0o600); err != nil {
			return fmt.Errorf("write default config: %w", err)
		}
	}
	return nil
}

// svcStop is the Windows service control code for stop.
// Avoids importing the full svc package just for this constant.
const svcStop = 1
