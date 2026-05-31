package codeagentutils

import (
	"fmt"
	"os"
	"path/filepath"
)

// LookPathNVM searches for a binary installed under NVM's node version dirs.
// It checks $NVM_DIR first, then ~/.nvm, and returns the most-recently-modified
// match so the active node version wins.
func LookPathNVM(name string) (string, error) {
	nvmDir := os.Getenv("NVM_DIR")
	if nvmDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		nvmDir = filepath.Join(home, ".nvm")
	}
	matches, err := filepath.Glob(filepath.Join(nvmDir, "versions", "node", "*", "bin", name))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("LookPathNVM: %s not found under %s", name, nvmDir)
	}
	best := ""
	var bestMod int64
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && !info.IsDir() {
			if info.ModTime().Unix() > bestMod {
				bestMod = info.ModTime().Unix()
				best = m
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("LookPathNVM: no executable %s found", name)
	}
	return best, nil
}
