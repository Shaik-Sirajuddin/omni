package operator

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed all:templates/**
var templateFS embed.FS

const (
	LatestVersion = "v2"
	MemoryDirName = "memory"
	metadataFile  = "metadata.yaml"
)

const (
	specialTemplatePrefix = "_"
	locationWorkspace     = "workspace"
	locationRoot          = "root"
	locationAgent         = "agent"
)

func requiredAgentDirsForVersion(version string) []string {
	if version == "v2" {
		return []string{"instructions", "tasks", "gen", "state"}
	}
	return []string{
		filepath.Join("entry", "instructions"),
		filepath.Join("entry", "tasks"),
		"generated",
		"state",
	}
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

	if err := seedMemoryRoot(memDir, LatestVersion); err != nil {
		logger.Error("memory.Init: seed root failed", "memoryDir", memDir, "version", LatestVersion, "err", err)
		return err
	}
	logger.Info("memory.Init: completed", "memoryDir", memDir, "version", LatestVersion)
	return nil
}

func (m *defaultAgentMemory) Create(memDir string) error {
	logger.Info("memory.Create: start", "memoryDir", memDir, "version", LatestVersion)
	workspaceRoot := workspaceRootFromAgentDir(memDir)
	agentName := filepath.Base(memDir)
	if err := applyTemplate(workspaceRoot, memDir, agentName, LatestVersion); err != nil {
		logger.Error("memory.Create: apply template failed", "memoryDir", memDir, "version", LatestVersion, "err", err)
		return err
	}
	logger.Info("memory.Create: completed", "memoryDir", memDir, "version", LatestVersion)
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
func applyTemplate(workspaceRoot, destDir, agentName, version string) error {
	logger.Debug("applyTemplate: start", "workspaceRoot", workspaceRoot, "destDir", destDir, "agentName", agentName, "version", version)
	for _, dir := range requiredAgentDirsForVersion(version) {
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
	if err := writeAgentWorkspaceDoc(ctx, version); err != nil {
		return err
	}
	if err := writeAgentCollabTasksDir(ctx, version); err != nil {
		return err
	}
	if vc := versionCode(version); vc >= 2 {
		// workspaceRoot here is already the memory dir (<ws>/memory),
		// not the repository root, so pass it directly without re-joining MemoryDirName.
		if err := stampAgentInLock(workspaceRoot, agentName, vc); err != nil {
			logger.Error("applyTemplate: stamp lock failed", "agentName", agentName, "version", version, "err", err)
			return err
		}
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
	case locationAgent:
		return filepath.Join(ctx.AgentRoot, fileName), nil
	default:
		return "", fmt.Errorf("unknown special template location %q", location)
	}
}

func renderTemplateName(value string, ctx templateContext) string {
	return strings.ReplaceAll(value, "<agent_name>", ctx.AgentName)
}

func writeAgentWorkspaceDoc(ctx templateContext, version string) error {
	var dest, content string
	if version == "v2" {
		// v2: agent profile lives inside the agent dir as agent.md
		dest = filepath.Join(ctx.AgentRoot, "agent.md")
		content = renderTemplateName("```yaml\nAGENT_NAME = <agent_name>\nmemory/agents/<agent_name>/\n\nread memory/memory.yaml\n\nto navigate project read agents_specification.md\n```\n", ctx)
	} else {
		// v1: flat workspace-root doc
		dest = filepath.Join(ctx.WorkspaceRoot, "agent_"+ctx.AgentName+".md")
		content = renderTemplateName("```yaml\nAGENT_NAME = <agent_name>\nmemory/<agent_name>/\n\nread memory/memory.yaml\n\nto navigate project read agents_specification.md\n```\n", ctx)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		logger.Error("writeAgentWorkspaceDoc: mkdir parent failed", "dest", dest, "err", err)
		return err
	}
	logger.Debug("writeAgentWorkspaceDoc: write file", "dest", dest)
	return os.WriteFile(dest, []byte(content), 0o644)
}

// writeAgentCollabTasksDir creates the agent's collab tasks folder inside the workspace.
// v2: memory/team/tasks/<agentName>/default.yaml
// v1: memory/team/entry/tasks/<agentName>/default.yaml
func writeAgentCollabTasksDir(ctx templateContext, version string) error {
	var dir string
	if version == "v2" {
		dir = filepath.Join(ctx.WorkspaceRoot, "team", "tasks", ctx.AgentName)
	} else {
		dir = filepath.Join(ctx.WorkspaceRoot, "team", "entry", "tasks", ctx.AgentName)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Error("writeAgentCollabTasksDir: mkdir failed", "dir", dir, "err", err)
		return err
	}
	dest := filepath.Join(dir, "default.yaml")
	content := "task_group_name: default\nauthor: " + ctx.AgentName + "\ninstructions:\n  -\n"
	logger.Debug("writeAgentCollabTasksDir: write file", "dest", dest)
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
	if vc := versionCode(version); vc >= 2 {
		if err := seedLockFile(memDir, vc); err != nil {
			logger.Error("seedMemoryRoot: seed lock failed", "memoryDir", memDir, "version", version, "err", err)
			return err
		}
	}
	logger.Debug("seedMemoryRoot: completed", "memoryDir", memDir, "version", version)
	return nil
}

const lockFileName = ".memory.lock"

// memoryLock is a minimal representation of the lock file shared with
// memory/tools/migrate. Only the fields needed for seeding are included.
type memoryLock struct {
	LockVersion   int            `yaml:"lock_version"`
	LayoutVersion int            `yaml:"layout_version"`
	TeamVersion   int            `yaml:"team_version"`
	MigratedAt    time.Time      `yaml:"migrated_at"`
	Agents        map[string]int `yaml:"agents"`
}

// seedLockFile writes an initial .memory.lock when creating a v2+ workspace.
// Skips if the file already exists (idempotent).
func seedLockFile(memDir string, layoutVersion int) error {
	path := filepath.Join(memDir, lockFileName)
	if _, err := os.Stat(path); err == nil {
		return nil // already present — leave it to the migrate tool
	}
	lf := memoryLock{
		LockVersion:   1,
		LayoutVersion: layoutVersion,
		TeamVersion:   layoutVersion,
		MigratedAt:    time.Now().UTC().Truncate(time.Second),
		Agents:        map[string]int{},
	}
	return writeLockFile(path, lf)
}

// stampAgentInLock records the given agent at layoutVersion in .memory.lock.
// Creates the lock file if absent; no-ops if the agent is already at that version.
func stampAgentInLock(memDir, agentName string, layoutVersion int) error {
	path := filepath.Join(memDir, lockFileName)

	// Read existing lock or start fresh.
	lf := memoryLock{
		LockVersion:   1,
		LayoutVersion: layoutVersion,
		TeamVersion:   layoutVersion,
		MigratedAt:    time.Now().UTC().Truncate(time.Second),
		Agents:        map[string]int{},
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(data, &lf)
		if lf.Agents == nil {
			lf.Agents = map[string]int{}
		}
	}

	if lf.Agents[agentName] >= layoutVersion {
		return nil // already current
	}
	lf.Agents[agentName] = layoutVersion
	lf.MigratedAt = time.Now().UTC().Truncate(time.Second)
	return writeLockFile(path, lf)
}

func writeLockFile(path string, lf memoryLock) error {
	header := []byte("# GENERATED — do not edit. Run memory/tools/migrate to update.\n")
	body, err := yaml.Marshal(lf)
	if err != nil {
		return fmt.Errorf("marshal lock: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(header, body...), 0o644); err != nil {
		return fmt.Errorf("write lock tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename lock: %w", err)
	}
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

// AgentMemDir returns the memory directory path for an agent inside its workspace.
func AgentMemDir(workspaceRoot, agentID string) string {
	return filepath.Join(workspaceRoot, MemoryDirName, "agents", agentID)
}

// NewDefaultAgentMemory returns the default embedded-template AgentMemory implementation.
func NewDefaultAgentMemory() AgentMemory {
	return &defaultAgentMemory{}
}

func workspaceRootFromAgentDir(memDir string) string {
	return filepath.Dir(filepath.Dir(memDir))
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

// memoryTemplateSchemaVersion is the fixed outer directory under templates/memory/.
// It is separate from LatestVersion so bumping the agent layout version does not
// change the template lookup path.
const memoryTemplateSchemaVersion = "v1"

func memoryTemplateRoot(version string) string {
	return filepath.Join("templates", "memory", memoryTemplateSchemaVersion, version)
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
