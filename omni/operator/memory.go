package operator

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const versionConfigFile = "version.yaml"

//go:embed all:templates/**
var templateFS embed.FS

const (
	LatestVersion = "v3"
	MemoryDirName = "memory"
	metadataFile  = "metadata.yaml"

	// memoryTemplateNamespace is the top-level directory under templates/memory/
	// that contains all per-version memory template sub-directories.
	// It is intentionally pinned to "v1" and decoupled from LatestVersion so
	// that adding new agent layout versions does not move the template storage root.
	memoryTemplateNamespace = "v1"
)

const (
	specialTemplatePrefix = "_"
	locationWorkspace     = "workspace"
	locationRoot          = "root"
)

// layoutConfig holds the version-specific directory and collab path rules for
// a single layout version.  All path components are relative to the agent's
// parent directory (the memory root) unless noted otherwise.
type layoutConfig struct {
	// agentSubDir is the sub-path from the memory root to the agent directory.
	// v1/v2: "agents/<name>",  v3: "<name>" (agent lives directly under memory/).
	agentSubDir func(agentName string) string
	// requiredDirs are the sub-paths inside the agent directory that must exist.
	requiredDirs []string
	// collabTasksDir returns the collab tasks directory path for the agent,
	// relative to the memory root.
	collabTasksDir func(agentName string) string
	// writeWorkspaceDoc controls whether an agent_<name>.md file should be
	// written at the memory root (v1/v2 yes, v3 no — roster files are forbidden).
	writeWorkspaceDoc bool
	// sysInstructionsRel is the path to the agent's system instructions,
	// relative to the agent directory.
	// v1: "entry/instructions" (dir), v2: "instructions" (dir),
	// v3: "instructions/memory.md" (file — primary instructions circuit).
	sysInstructionsRel string
}

// SysInstructionsPath returns the absolute path to the agent's system
// instructions for this layout version.  agentDir must be the resolved
// on-disk memory directory for the agent (e.g. from ResolveAgentMemDir).
func (l layoutConfig) SysInstructionsPath(agentDir string) string {
	return filepath.Join(agentDir, l.sysInstructionsRel)
}

// versionLayouts maps version codes to their layout configurations.
// v1 and v2 are frozen; v3 is current.
var versionLayouts = map[int]layoutConfig{
	1: {
		agentSubDir: func(name string) string {
			return filepath.Join("agents", name)
		},
		requiredDirs: []string{
			filepath.Join("entry", "instructions"),
			filepath.Join("entry", "tasks"),
			"generated",
			"state",
		},
		collabTasksDir: func(name string) string {
			return filepath.Join("team", "entry", "tasks", name)
		},
		writeWorkspaceDoc:  true,
		sysInstructionsRel: filepath.Join("entry", "instructions"),
	},
	2: {
		agentSubDir: func(name string) string {
			return filepath.Join("agents", name)
		},
		requiredDirs: []string{
			"instructions",
			"tasks",
			"gen",
			"state",
		},
		collabTasksDir: func(name string) string {
			return filepath.Join("team", "tasks", name)
		},
		writeWorkspaceDoc:  true,
		sysInstructionsRel: filepath.Join("instructions", "memory.md"),
	},
	3: {
		agentSubDir: func(name string) string {
			return name
		},
		requiredDirs: []string{
			"instructions",
			"skills",
			"tasks",
			filepath.Join("knowledge", "com"),
			filepath.Join("gen", "plans"),
			filepath.Join("gen", "state"),
		},
		collabTasksDir: func(name string) string {
			return filepath.Join("collab", "tasks", name)
		},
		writeWorkspaceDoc:  false,
		sysInstructionsRel: filepath.Join("instructions", "memory.md"),
	},
}

// layoutForVersion returns the layoutConfig for the given version code.
// For unknown version codes it falls back to the highest known version layout
// (currently v3) rather than v1, because an unrecognised code is far more
// likely to be a future version than a legacy one.  A warning is logged.
func layoutForVersion(code int) layoutConfig {
	if cfg, ok := versionLayouts[code]; ok {
		return cfg
	}
	// Find the highest known version to use as fallback.
	highest := 0
	for k := range versionLayouts {
		if k > highest {
			highest = k
		}
	}
	logger.Warn("layoutForVersion: unknown version code, falling back to highest known layout",
		"requestedCode", code, "fallbackCode", highest)
	return versionLayouts[highest]
}

// latestEmbeddedVersion returns the version short-name (e.g. "v3") of the
// highest-numbered agent template embedded in the binary.  It iterates over
// versionLayouts and returns the entry whose template files both exist in the
// embedded FS.
func latestEmbeddedVersion() string {
	highest := 0
	for code := range versionLayouts {
		v := fmt.Sprintf("v%d", code)
		if templateExists(v) && code > highest {
			highest = code
		}
	}
	if highest == 0 {
		return LatestVersion
	}
	return fmt.Sprintf("v%d", highest)
}

// defaultVersion resolves the default layout version for the given memory
// root directory.
//
//   - If <memDir>/version.yaml exists and contains a non-empty
//     `default_version:` key, that value is used.
//   - Otherwise the highest embedded template version is used (computed by
//     latestEmbeddedVersion).
func defaultVersion(memDir string) string {
	data, err := os.ReadFile(filepath.Join(memDir, versionConfigFile))
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "default_version:") {
				v := strings.TrimSpace(strings.TrimPrefix(line, "default_version:"))
				// strip inline YAML comments
				if idx := strings.Index(v, "#"); idx >= 0 {
					v = strings.TrimSpace(v[:idx])
				}
				if v != "" {
					return v
				}
			}
		}
	}
	return latestEmbeddedVersion()
}

// discoverAvailableVersions lists version codes present as memory/v<x>/ dirs
// under the given memory root.  Returns nil if none are found.
func discoverAvailableVersions(memRoot string) []int {
	entries, err := os.ReadDir(memRoot)
	if err != nil {
		return nil
	}
	re := regexp.MustCompile(`^v(\d+)$`)
	var codes []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if c, err := strconv.Atoi(m[1]); err == nil {
			codes = append(codes, c)
		}
	}
	return codes
}

// AgentMemory is the pluggable module for managing agent memory on disk.
// Swap the implementation (e.g. for testing) by passing a different value to
// DefaultOperator.agentMemory. A nil value means the memory feature is disabled.
type AgentMemory interface {
	// Init initialises the workspace memory root.
	// When repoURL is non-empty the memory dir is added as a git submodule;
	// otherwise an empty local git repo is initialised inside memory/.
	// Memory is seeded with the current template in either case.
	Init(workspaceRoot, repoURL string) error

	// Create seeds the latest embedded template into a newly created agent's memory dir.
	Create(memDir string) error

	// Upgrade applies a (newer) template version to an existing agent memory dir.
	// Non-destructive: files not part of the template are left untouched.
	Upgrade(memDir, version string) error

	// Delete removes the agent memory directory from disk.
	Delete(memDir string) error
}

// defaultAgentMemory is the embedded-template implementation of AgentMemory.
type defaultAgentMemory struct{}

func (m *defaultAgentMemory) Init(workspaceRoot, repoURL string) error {
	memDir := filepath.Join(workspaceRoot, MemoryDirName)
	logger.Info("memory.Init: start", "workspaceRoot", workspaceRoot, "repoURL", repoURL, "memoryDir", memDir)

	if repoURL != "" {
		if err := gitSubmoduleAdd(workspaceRoot, repoURL, MemoryDirName); err != nil {
			logger.Error("memory.Init: git submodule add failed", "workspaceRoot", workspaceRoot, "repoURL", repoURL, "err", err)
			return fmt.Errorf("git submodule add: %w", err)
		}
	} else {
		if err := os.MkdirAll(memDir, 0o755); err != nil {
			logger.Error("memory.Init: mkdir failed", "memoryDir", memDir, "err", err)
			return fmt.Errorf("mkdir memory: %w", err)
		}
		if !isGitRepo(memDir) {
			if err := gitInit(memDir); err != nil {
				logger.Error("memory.Init: git init failed", "memoryDir", memDir, "err", err)
				return fmt.Errorf("git init: %w", err)
			}
		}
	}

	version := defaultVersion(memDir)
	if err := seedMemoryRoot(memDir, version); err != nil {
		logger.Error("memory.Init: seed root failed", "memoryDir", memDir, "version", version, "err", err)
		return err
	}
	// Pin the resolved default so that later `memory.Create` calls use the same
	// version without re-computing it each time.
	if err := writeVersionConfig(memDir, version); err != nil {
		logger.Error("memory.Init: write version config failed", "memoryDir", memDir, "version", version, "err", err)
		return err
	}
	logger.Info("memory.Init: completed", "memoryDir", memDir, "version", version)
	return nil
}

func (m *defaultAgentMemory) Create(memDir string) error {
	workspaceRoot := workspaceRootFromAgentDir(memDir)
	version := defaultVersion(workspaceRoot)
	logger.Info("memory.Create: start", "memoryDir", memDir, "version", version)
	agentName := filepath.Base(memDir)
	if err := applyTemplate(workspaceRoot, memDir, agentName, version); err != nil {
		logger.Error("memory.Create: apply template failed", "memoryDir", memDir, "version", version, "err", err)
		return err
	}
	logger.Info("memory.Create: completed", "memoryDir", memDir, "version", version)
	return nil
}

func (m *defaultAgentMemory) Upgrade(memDir, version string) error {
	logger.Info("memory.Upgrade: start", "memoryDir", memDir, "version", version)
	if !templateExists(version) {
		logger.Error("memory.Upgrade: unknown template version", "memoryDir", memDir, "version", version)
		return fmt.Errorf("unknown template version %q", version)
	}
	current, err := readVersionCode(memDir)
	if err == nil && current >= versionCode(version) {
		logger.Error("memory.Upgrade: upgrade not needed", "memoryDir", memDir, "currentVersionCode", current, "targetVersion", version)
		return fmt.Errorf("already at version %s (code %d)", version, current)
	}
	workspaceRoot := workspaceRootFromAgentDir(memDir)
	agentName := filepath.Base(memDir)
	if err := applyTemplate(workspaceRoot, memDir, agentName, version); err != nil {
		logger.Error("memory.Upgrade: apply template failed", "memoryDir", memDir, "version", version, "err", err)
		return err
	}
	logger.Info("memory.Upgrade: completed", "memoryDir", memDir, "version", version)
	return nil
}

func (m *defaultAgentMemory) Delete(memDir string) error {
	logger.Info("memory.Delete: start", "memoryDir", memDir)
	if err := os.RemoveAll(memDir); err != nil {
		logger.Error("memory.Delete: remove failed", "memoryDir", memDir, "err", err)
		return err
	}
	logger.Info("memory.Delete: completed", "memoryDir", memDir)
	return nil
}

// --- template helpers ---

// applyTemplate copies the embedded template for the given version into destDir.
// Existing files not part of the template are left untouched (non-destructive).
// workspaceRoot here is the memory root (e.g. /path/to/workspace/memory).
func applyTemplate(workspaceRoot, destDir, agentName, version string) error {
	logger.Debug("applyTemplate: start", "workspaceRoot", workspaceRoot, "destDir", destDir, "agentName", agentName, "version", version)

	layout := layoutForVersion(versionCode(version))
	for _, dir := range layout.requiredDirs {
		dest := filepath.Join(destDir, dir)
		if err := os.MkdirAll(dest, 0o755); err != nil {
			logger.Error("applyTemplate: ensure required dir failed", "dest", dest, "err", err)
			return err
		}
	}

	ctx := templateContext{
		WorkspaceRoot: workspaceRoot,
		AgentRoot:     destDir,
		AgentName:     agentName,
	}

	if err := copyTemplateTree(agentTemplateRoot(version), destDir, ctx); err != nil {
		return err
	}
	if layout.writeWorkspaceDoc {
		if err := writeAgentWorkspaceDoc(ctx); err != nil {
			return err
		}
	}
	if err := writeAgentCollabTasksDir(ctx, layout); err != nil {
		return err
	}
	return nil
}

type templateContext struct {
	WorkspaceRoot string
	AgentRoot     string
	AgentName     string
}

func copyTemplateTree(root, destDir string, ctx templateContext) error {
	logger.Debug("copyTemplateTree: start", "root", root, "destDir", destDir)
	return fs.WalkDir(templateFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			logger.Error("copyTemplateTree: walk failed", "path", path, "err", err)
			return err
		}
		if path == root {
			return nil
		}
		rel := strings.TrimPrefix(path, root+"/")
		if strings.HasPrefix(filepath.Base(rel), specialTemplatePrefix) {
			if d.IsDir() {
				return nil
			}
			return writeSpecialTemplate(path, rel, ctx)
		}
		dest := filepath.Join(destDir, rel)
		if d.IsDir() {
			logger.Debug("copyTemplateTree: ensure dir", "dest", dest)
			return os.MkdirAll(dest, 0o755)
		}
		data, err := templateFS.ReadFile(path)
		if err != nil {
			logger.Error("copyTemplateTree: read file failed", "path", path, "err", err)
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			logger.Error("copyTemplateTree: mkdir parent failed", "dest", dest, "err", err)
			return err
		}
		logger.Debug("copyTemplateTree: write file", "dest", dest)
		return os.WriteFile(dest, data, 0o644)
	})
}

func writeSpecialTemplate(path, rel string, ctx templateContext) error {
	dest, err := resolveSpecialTemplatePath(rel, ctx)
	if err != nil {
		logger.Error("writeSpecialTemplate: resolve path failed", "template", rel, "err", err)
		return err
	}
	data, err := templateFS.ReadFile(path)
	if err != nil {
		logger.Error("writeSpecialTemplate: read file failed", "path", path, "err", err)
		return err
	}
	rendered := renderTemplateName(string(data), ctx)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		logger.Error("writeSpecialTemplate: mkdir parent failed", "dest", dest, "err", err)
		return err
	}
	logger.Debug("writeSpecialTemplate: write file", "dest", dest)
	return os.WriteFile(dest, []byte(rendered), 0o644)
}

func resolveSpecialTemplatePath(rel string, ctx templateContext) (string, error) {
	name := strings.TrimPrefix(filepath.Base(rel), specialTemplatePrefix)
	parts := strings.Split(name, "_")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid special template path %q", rel)
	}

	location := parts[0]
	fileName := renderTemplateName(strings.Join(parts[1:], "_"), ctx)

	switch location {
	case locationWorkspace, locationRoot:
		return filepath.Join(ctx.WorkspaceRoot, fileName), nil
	default:
		return "", fmt.Errorf("unknown special template location %q", location)
	}
}

func renderTemplateName(value string, ctx templateContext) string {
	return strings.ReplaceAll(value, "<agent_name>", ctx.AgentName)
}

func writeAgentWorkspaceDoc(ctx templateContext) error {
	dest := filepath.Join(ctx.WorkspaceRoot, "agent_"+ctx.AgentName+".md")
	content := renderTemplateName("```yaml\nAGENT_NAME = <agent_name>\nmemory/<agent_name>/\n\nread memory/memory.yaml\n\nto navigate project read agents_specification.md\n```\n", ctx)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		logger.Error("writeAgentWorkspaceDoc: mkdir parent failed", "dest", dest, "err", err)
		return err
	}
	logger.Debug("writeAgentWorkspaceDoc: write file", "dest", dest)
	return os.WriteFile(dest, []byte(content), 0o644)
}

// writeAgentCollabTasksDir creates the agent's collab tasks folder and seeds a
// default.yaml stub.  The exact path is determined by the layout version:
//   - v1: memory/team/entry/tasks/<agent>/default.yaml
//   - v2: memory/team/tasks/<agent>/default.yaml
//   - v3: memory/collab/tasks/<agent>/default.yaml
//
// ctx.WorkspaceRoot is the memory root directory.
func writeAgentCollabTasksDir(ctx templateContext, layout layoutConfig) error {
	dir := filepath.Join(ctx.WorkspaceRoot, layout.collabTasksDir(ctx.AgentName))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Error("writeAgentCollabTasksDir: mkdir failed", "dir", dir, "err", err)
		return err
	}
	dest := filepath.Join(dir, "default.yaml")
	content := "task_group_name: default\nauthor: " + ctx.AgentName + "\ninstructions:\n  -\n"
	logger.Debug("writeAgentCollabTasksDir: write file", "dest", dest)
	return os.WriteFile(dest, []byte(content), 0o644)
}

// writeVersionConfig writes (or overwrites) version.yaml at the memory root
// with the supplied default_version value.
func writeVersionConfig(memDir, version string) error {
	dest := filepath.Join(memDir, versionConfigFile)
	content := fmt.Sprintf("default_version: %s\n", version)
	logger.Debug("writeVersionConfig: write", "dest", dest, "version", version)
	return os.WriteFile(dest, []byte(content), 0o644)
}

// seedMemoryRoot creates agents/ and writes metadata.yaml at the memory root.
func seedMemoryRoot(memDir, version string) error {
	logger.Debug("seedMemoryRoot: start", "memoryDir", memDir, "version", version)
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		logger.Error("seedMemoryRoot: mkdir memory failed", "memoryDir", memDir, "err", err)
		return err
	}
	meta := fmt.Sprintf("version: %s\nversion_code: %d\n", version, versionCode(version))
	if err := os.WriteFile(filepath.Join(memDir, metadataFile), []byte(meta), 0o644); err != nil {
		logger.Error("seedMemoryRoot: write metadata failed", "memoryDir", memDir, "err", err)
		return err
	}
	ctx := templateContext{WorkspaceRoot: filepath.Dir(memDir)}
	if err := copyTemplateTree(memoryTemplateRoot(version), memDir, ctx); err != nil {
		return err
	}
	logger.Debug("seedMemoryRoot: completed", "memoryDir", memDir, "version", version)
	return nil
}

func templateExists(version string) bool {
	_, agentErr := templateFS.Open(agentTemplateRoot(version))
	_, memoryErr := templateFS.Open(memoryTemplateRoot(version))
	return agentErr == nil && memoryErr == nil
}

func versionCode(version string) int {
	s := strings.TrimPrefix(version, "v")
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

func readVersionCode(memDir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(memDir, metadataFile))
	if err != nil {
		logger.Error("readVersionCode: read metadata failed", "memoryDir", memDir, "err", err)
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "version_code:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "version_code:"))
			n := 0
			for _, c := range val {
				if c >= '0' && c <= '9' {
					n = n*10 + int(c-'0')
				}
			}
			return n, nil
		}
	}
	logger.Error("readVersionCode: version code missing", "memoryDir", memDir, "file", metadataFile)
	return 0, fmt.Errorf("version_code not found in %s", metadataFile)
}

// AgentMemDir returns the memory directory path for an agent using the
// LatestVersion layout.  Callers that need to locate an existing agent whose
// version may differ from the latest should use AgentMemDirResolved instead.
func AgentMemDir(workspaceRoot, agentID string) string {
	return AgentMemDirForVersion(workspaceRoot, agentID, versionCode(LatestVersion))
}

// AgentMemDirForVersion returns the memory directory path for an agent using
// the layout rules for the given version code.  This allows callers to resolve
// existing agents whose layout version may differ from LatestVersion.
//
// Version discovery order for existing agents:
//  1. Read version_code from <memRoot>/<agentSubDir>/metadata.yaml.
//  2. Fall back to structural inference via inferAgentVersion.
//  3. Default to LatestVersion when neither source is available.
//
// When the code parameter is <= 0 the function reads the version from disk
// using the above order (pass versionCode(LatestVersion) to force the latest).
func AgentMemDirForVersion(workspaceRoot, agentID string, code int) string {
	if code <= 0 {
		code = versionCode(LatestVersion)
	}
	layout := layoutForVersion(code)
	memRoot := filepath.Join(workspaceRoot, MemoryDirName)
	return filepath.Join(memRoot, layout.agentSubDir(agentID))
}

// ResolveAgentSysInstructionsPath resolves the filesystem path to an agent's
// system instructions using the agent's on-disk layout version.
//
// Version mapping:
//
//	v1 → <agentdir>/entry/instructions  (directory)
//	v2 → <agentdir>/instructions/memory.md
//	v3 → <agentdir>/instructions/memory.md
//
// Returns an error when the agent directory cannot be found on disk.
func ResolveAgentSysInstructionsPath(workspaceRoot, agentID string) (string, error) {
	memRoot := filepath.Join(workspaceRoot, MemoryDirName)

	// Try each known version's agent sub-path (newest first).
	for _, c := range []int{3, 2, 1} {
		cfg := layoutForVersion(c)
		candidate := filepath.Join(memRoot, cfg.agentSubDir(agentID))
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		// Use declared version_code from metadata.yaml to choose the instructions
		// sub-path, but keep `candidate` (the on-disk location) as the agent dir.
		resolvedCfg := cfg
		if declaredCode, err := readVersionCode(candidate); err == nil && declaredCode > 0 {
			resolvedCfg = layoutForVersion(declaredCode)
		}
		return resolvedCfg.SysInstructionsPath(candidate), nil
	}
	return "", fmt.Errorf("agent %q not found under %s", agentID, memRoot)
}

// ResolveAgentMemDir returns the on-disk memory directory for an existing agent
// by auto-detecting its layout version from metadata.yaml or directory structure.
// Falls back to the LatestVersion path when detection is inconclusive.
func ResolveAgentMemDir(workspaceRoot, agentID string) string {
	memRoot := filepath.Join(workspaceRoot, MemoryDirName)

	// Try each known version's agent sub-path to find one that exists on disk.
	// Check in reverse (newest first) so the most-current layout wins when
	// multiple could match.  We always return the on-disk `candidate` path
	// directly; the declared version_code is only used to choose layout config
	// for sub-path resolution elsewhere — not to recompute the agent dir itself.
	for _, code := range []int{3, 2, 1} {
		cfg := layoutForVersion(code)
		candidate := filepath.Join(memRoot, cfg.agentSubDir(agentID))
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		// Return the actual on-disk path regardless of what metadata.yaml declares.
		return candidate
	}
	// Fallback: construct the path using the latest version layout.
	return AgentMemDir(workspaceRoot, agentID)
}

// NewDefaultAgentMemory returns the default embedded-template AgentMemory implementation.
func NewDefaultAgentMemory() AgentMemory {
	return &defaultAgentMemory{}
}

// workspaceRootFromAgentDir returns the memory root directory (e.g.
// /workspace/memory) given an agent memory directory.
//
// Layout-aware depth:
//   - v1/v2: memDir = memory/agents/<name>  → memory root = Dir(Dir(memDir))
//   - v3:    memDir = memory/<name>         → memory root = Dir(memDir)
//
// Detection: if the direct parent of memDir is named "agents" we apply the
// v1/v2 depth; otherwise the v3 depth (one Dir only).
func workspaceRootFromAgentDir(memDir string) string {
	parent := filepath.Dir(memDir)
	if filepath.Base(parent) == "agents" {
		// v1/v2: memory/agents/<name> → memory/
		return filepath.Dir(parent)
	}
	// v3: memory/<name> → memory/
	return parent
}

// ListTemplateVersions returns the version short-name from each embedded agent template.
func ListTemplateVersions() ([]string, error) {
	entries, err := templateFS.ReadDir("templates/agents")
	if err != nil {
		return nil, fmt.Errorf("operator: list templates: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := templateFS.ReadFile("templates/agents/" + e.Name() + "/" + metadataFile)
		if err != nil {
			logger.Warn("ListTemplateVersions: skipping template with unreadable metadata", "template", e.Name(), "err", err)
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "version:") {
				v := strings.TrimSpace(strings.TrimPrefix(line, "version:"))
				// strip inline YAML comments
				if idx := strings.Index(v, "#"); idx >= 0 {
					v = strings.TrimSpace(v[:idx])
				}
				if v != "" {
					names = append(names, v)
				}
			}
		}
	}
	return names, nil
}

func agentTemplateRoot(version string) string {
	return filepath.Join("templates", "agents", version)
}

func memoryTemplateRoot(version string) string {
	return filepath.Join("templates", "memory", memoryTemplateNamespace, version)
}

// --- git helpers ---

func gitSubmoduleAdd(workspaceRoot, repoURL, submodulePath string) error {
	logger.Debug("gitSubmoduleAdd: running", "workspaceRoot", workspaceRoot, "repoURL", repoURL, "submodulePath", submodulePath)
	return runGit(workspaceRoot, "submodule", "add", repoURL, submodulePath)
}

func gitInit(dir string) error {
	logger.Debug("gitInit: running", "dir", dir)
	return runGit(dir, "init")
}

func isGitRepo(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
}

func runGit(dir string, args ...string) error {
	logger.Debug("runGit: exec", "dir", dir, "args", args)
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("runGit: command failed", "dir", dir, "args", args, "err", err, "output", strings.TrimSpace(string(out)))
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	logger.Debug("runGit: command completed", "dir", dir, "args", args)
	return nil
}
