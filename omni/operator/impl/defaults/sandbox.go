package defaults

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox"
	"github.com/Shaik-Sirajuddin/memory/sandbox/common"
)

// Binary candidate lists mirror the respective connector configs so that
// this package does not need to import the connector packages (some of which
// have pre-existing build issues in their generated files).
var claudeBinaries = []string{
	"claude",
	"/usr/local/bin/claude",
	"/opt/homebrew/bin/claude",
}

var geminiBinaries = []string{
	"gemini",
	"/usr/local/bin/gemini",
	"/opt/homebrew/bin/gemini",
}

var agyBinaries = []string{
	"agy",
	"/usr/local/bin/agy",
	"/opt/homebrew/bin/agy",
	// expanded at call-time via os.ExpandEnv in BinaryDirs
	"$HOME/.local/bin/agy",
}

// BinaryDirs returns the unique parent directories that contain the resolved
// binary for the given provider.  Candidates are resolved via exec.LookPath;
// absolute hard-coded paths are included directly when look-up fails.
func BinaryDirs(provider codeagent.Provider) []string {
	var candidates []string
	switch provider {
	case "claude":
		candidates = claudeBinaries
	case "gemini":
		candidates = geminiBinaries
	case "agy":
		candidates = agyBinaries
	default:
		// codex and unknown providers: resolve the provider name from PATH.
		if p, err := exec.LookPath(string(provider)); err == nil {
			return []string{filepath.Dir(p)}
		}
		return nil
	}

	seen := map[string]struct{}{}
	var dirs []string
	for _, bin := range candidates {
		bin = os.ExpandEnv(bin)
		resolved, err := exec.LookPath(bin)
		if err != nil {
			// Absolute fallback paths are included even when not present.
			if filepath.IsAbs(bin) {
				d := filepath.Dir(bin)
				if _, ok := seen[d]; !ok {
					seen[d] = struct{}{}
					dirs = append(dirs, d)
				}
			}
			continue
		}
		d := filepath.Dir(resolved)
		if _, ok := seen[d]; !ok {
			seen[d] = struct{}{}
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// SandboxConfig returns the default sandbox.Config for the given provider
// scoped to workspaceDir. The workspace policy grants permissive-read access
// to the real workspace directory. The agent policy additionally allows the
// directories containing the provider binary.
func SandboxConfig(provider codeagent.Provider, workspaceDir string) *sandbox.Config {
	cfg := common.AgentDefaultConfig(workspaceDir)
	binDirs := BinaryDirs(provider)
	if len(binDirs) > 0 && cfg.AgentPolicy != nil {
		cfg.AgentPolicy.Config.AccessDirs = append(cfg.AgentPolicy.Config.AccessDirs, binDirs...)
	}
	return cfg
}
