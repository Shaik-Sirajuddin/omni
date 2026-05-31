package hookoperator

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
)

// ProviderHookStatus is the verification result for one provider's transformer.
type ProviderHookStatus struct {
	Provider codeagent.Provider `json:"provider"`
	OK       bool               `json:"ok"`
	Missing  []string           `json:"missing"`
}

// HookRegistrar exposes hook status for all owned providers.
type HookRegistrar interface {
	Verify(provider codeagent.Provider) (bool, []string, error)
	Status() []ProviderHookStatus
}

type registrar struct {
	binaryPath string
}

// NewRegistrar creates a standalone registrar.
// When binaryPath is empty, os.Executable() is used.
func NewRegistrar(binaryPath string) (*registrar, error) {
	if binaryPath == "" {
		path, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("hook-operator: resolve binary: %w", err)
		}
		binaryPath = path
	}
	return &registrar{binaryPath: binaryPath}, nil
}

// apply adds all default hook entries to transformer. Already-registered names are skipped.
func (r *registrar) apply(transformer codeagent.HookTransformer) error {
	if transformer == nil {
		return errors.New("hook-operator: apply: transformer required")
	}
	for _, dh := range DefaultHooks(r.binaryPath) {
		if !transformer.Add(dh.Name, dh.Entry) {
			logger.Warn("hook-operator: register: hook not added", "name", dh.Name)
		} else {
			logger.Debug("hook-operator: register: hook added", "name", dh.Name)
		}
	}
	return nil
}

// verify checks whether all default hook entries are present on the transformer.
// GetHooks() now reads from the codeagent settings file and returns auto-generated
// names, so we match by entry content (command+args) rather than by name.
func (r *registrar) verify(transformer codeagent.HookTransformer) (bool, []string) {
	registered := make(map[string]struct{})
	for _, h := range transformer.GetHooks() {
		registered[entryKey(h.Entry)] = struct{}{}
	}
	var missing []string
	for _, dh := range DefaultHooks(r.binaryPath) {
		if _, ok := registered[entryKey(dh.Entry)]; !ok {
			missing = append(missing, dh.Name)
		}
	}
	return len(missing) == 0, missing
}

// entryKey produces a comparable string for a hook entry based on its command and args.
func entryKey(e config.HookEntry) string {
	cmd := ""
	if e.Command != nil {
		cmd = *e.Command
	}
	return cmd + "\x00" + strings.Join(e.Args, "\x00")
}
