package impl

import (
	"fmt"

	"github.com/Shaik-Sirajuddin/memory/operator"
	"github.com/Shaik-Sirajuddin/memory/terminal"
	"github.com/Shaik-Sirajuddin/memory/terminal/provider/zellij"
)

type registeredTerminal struct {
	name     string
	terminal terminal.Terminal
}

// terminalRegistry holds the set of known terminal providers keyed by name.
var terminalRegistry = []registeredTerminal{
	{name: "zellij", terminal: zellij.New()},
}

// getTerminal returns the terminal.Terminal for the given name, or an error if unknown.
func getTerminal(name string) (terminal.Terminal, error) {
	for _, r := range terminalRegistry {
		if r.name == name {
			return r.terminal, nil
		}
	}
	return nil, fmt.Errorf("operator: unknown terminal %q; supported: %v", name, supportedTerminalNames())
}

func supportedTerminalNames() []string {
	names := make([]string, len(terminalRegistry))
	for i, r := range terminalRegistry {
		names[i] = r.name
	}
	return names
}

// DoctorTerminalsStandalone runs terminal health checks without a running operator
// daemon. It reads terminalRegistry directly and requires no DB or socket.
func DoctorTerminalsStandalone() (*operator.DoctorTerminalsResult, error) {
	result := &operator.DoctorTerminalsResult{
		Terminals: make([]operator.TerminalStatus, 0, len(terminalRegistry)),
	}
	for _, r := range terminalRegistry {
		status := operator.TerminalStatus{Name: r.name, Installed: true}
		if err := r.terminal.CheckInstallation(); err != nil {
			status.Installed = false
			status.Error = err.Error()
		}
		result.Terminals = append(result.Terminals, status)
	}
	return result, nil
}

// InstallTerminalStandalone installs the named terminal provider without a
// running operator daemon. Returns an error if the name is not registered.
func InstallTerminalStandalone(name string) error {
	term, err := getTerminal(name)
	if err != nil {
		return err
	}
	return term.Install()
}
