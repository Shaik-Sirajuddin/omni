// Package memory provides a version-aware validator for omni memory trees.
//
// Usage:
//
//	report, err := memory.Validate("/path/to/memory", memory.Options{})
//	if err != nil { ... }
//	if report.HasViolations() { os.Exit(1) }
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Severity classifies a validation finding.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Violation is a single validation finding.
type Violation struct {
	Path     string   // file or directory path
	Severity Severity // error, warning, info
	Message  string
}

// Report is the result of a Validate call.
type Report struct {
	Version    int         // resolved version (0 = unknown)
	TargetPath string      // path that was validated
	Violations []Violation // all findings (any severity)
}

// HasViolations returns true when any error-level violation is present.
func (r Report) HasViolations() bool {
	for _, v := range r.Violations {
		if v.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Options controls Validate behaviour.
type Options struct {
	// ForceVersion overrides auto-detection when > 0 (e.g. 3 for v3).
	ForceVersion int
	// MemoryRoot is the directory that contains the v<x>/ contract dirs.
	// If empty it is inferred as the "memory" sibling of TargetPath, or
	// the target itself when it looks like a memory root.
	MemoryRoot string
	// AgentName restricts validation to a single named agent.
	AgentName string
	// Fix scaffolds missing memory.md files (v3 only) when true.
	Fix bool
}

// ─── Contract types ──────────────────────────────────────────────────────────

// contractMetadata is the parsed memory/v<x>/metadata.yaml.
type contractMetadata struct {
	VersionCode int    `yaml:"version_code"`
	Status      string `yaml:"status"`
}

// v1v2Layout is the parsed layout section for v1/v2 semantics.yaml.
type v1v2Layout struct {
	AgentRoot  string   `yaml:"agent_root"`
	AgentDirs  []string `yaml:"agent_dirs"`
	CollabRoot string   `yaml:"collab_root"`
}

// v1v2Semantics is the top-level v1/v2 semantics.yaml shape.
type v1v2Semantics struct {
	Layout v1v2Layout `yaml:"layout"`
}

// v3AgentDir is one entry in the v3 semantics agent_dirs list.
type v3AgentDir struct {
	Name            string       `yaml:"name"`
	RequiredFiles   []string     `yaml:"required_files"`
	RequiredSubdirs []v3AgentDir `yaml:"required_subdirs"`
}

// v3Layout is the v3 layout section.
type v3Layout struct {
	AgentFiles  []string     `yaml:"agent_files"`
	AgentDirs   []v3AgentDir `yaml:"agent_dirs"`
	AbsentInV3  []string     `yaml:"absent_in_v3"`
}

// v3Semantics is the top-level v3 semantics.yaml shape.
type v3Semantics struct {
	Layout v3Layout `yaml:"layout"`
}

// ─── Public API ──────────────────────────────────────────────────────────────

// Validate validates the target path (a whole memory tree or a single agent dir)
// against its versioned contract.  The contract dirs (memory/v<x>/) are located
// relative to MemoryRoot or inferred automatically.
func Validate(targetPath string, opts Options) (Report, error) {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return Report{}, fmt.Errorf("resolve path: %w", err)
	}

	// Identify memory root.
	memRoot, err := resolveMemoryRoot(absTarget, opts.MemoryRoot)
	if err != nil {
		return Report{}, err
	}

	// Discover available version contracts on disk.
	available, err := discoverVersions(memRoot)
	if err != nil {
		return Report{}, err
	}
	if len(available) == 0 {
		return Report{}, fmt.Errorf("no version contracts found under %s (expected memory/v<x>/ dirs)", memRoot)
	}

	report := Report{TargetPath: absTarget}

	// Determine whether we're validating a whole tree or a single agent dir.
	isAgentDir := isAgentDirectory(absTarget, memRoot)

	if opts.AgentName != "" {
		// Force single-agent mode by name. Agents may live at the v3 location
		// (<memRoot>/<name>) or the legacy v1/v2 location (<memRoot>/agents/<name>);
		// probe both so coexistence works in a mixed workspace.
		agentPath, ok := resolveAgentByName(memRoot, opts.AgentName)
		if !ok {
			return Report{}, fmt.Errorf("agent %q not found at %s or %s",
				opts.AgentName,
				filepath.Join(memRoot, opts.AgentName),
				filepath.Join(memRoot, "agents", opts.AgentName))
		}
		return validateSingleAgent(agentPath, memRoot, opts, available)
	}

	if isAgentDir {
		return validateSingleAgent(absTarget, memRoot, opts, available)
	}

	// Whole-tree mode: validate every top-level agent dir.
	return validateTree(absTarget, memRoot, opts, available, report)
}

// Lint performs format-only checks: every *.yaml under the memory tree parses,
// and every memory.md exists where expected.
func Lint(memoryRoot string) (Report, error) {
	absRoot, err := filepath.Abs(memoryRoot)
	if err != nil {
		return Report{}, fmt.Errorf("resolve path: %w", err)
	}

	report := Report{TargetPath: absRoot}

	err = filepath.Walk(absRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			report.addWarn(path, fmt.Sprintf("walk error: %v", walkErr))
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			if err := lintYAMLFile(path); err != nil {
				report.addError(path, err.Error())
			}
		}
		return nil
	})
	if err != nil {
		return report, fmt.Errorf("lint walk: %w", err)
	}
	return report, nil
}

// ─── Internal helpers ────────────────────────────────────────────────────────

func (r *Report) addError(path, msg string) {
	r.Violations = append(r.Violations, Violation{Path: path, Severity: SeverityError, Message: msg})
}

func (r *Report) addWarn(path, msg string) {
	r.Violations = append(r.Violations, Violation{Path: path, Severity: SeverityWarning, Message: msg})
}

// resolveMemoryRoot returns the directory that contains v<x>/ contract dirs.
// If memRootHint is non-empty it is used directly. Otherwise it tries:
//  1. target itself (if it contains a v<x>/ subdir)
//  2. "memory" subdir of target
//  3. "memory" sibling of target
func resolveMemoryRoot(absTarget, memRootHint string) (string, error) {
	if memRootHint != "" {
		abs, err := filepath.Abs(memRootHint)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	// Check target itself.
	if hasVersionDir(absTarget) {
		return absTarget, nil
	}
	// Check memory/ subdir of target.
	sub := filepath.Join(absTarget, "memory")
	if hasVersionDir(sub) {
		return sub, nil
	}
	// Check memory/ as sibling (target is an agent inside memory/).
	parent := filepath.Dir(absTarget)
	if hasVersionDir(parent) {
		return parent, nil
	}
	return "", fmt.Errorf("cannot locate memory root from %s (no v<x>/ contract dirs found)", absTarget)
}

// hasVersionDir returns true if dir contains at least one v<x>/ subdir.
func hasVersionDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	re := regexp.MustCompile(`^v\d+$`)
	for _, e := range entries {
		if e.IsDir() && re.MatchString(e.Name()) {
			return true
		}
	}
	return false
}

// discoverVersions lists available version codes from memory/v<x>/ dirs.
func discoverVersions(memRoot string) (map[int]string, error) {
	entries, err := os.ReadDir(memRoot)
	if err != nil {
		return nil, fmt.Errorf("read memory root %s: %w", memRoot, err)
	}
	re := regexp.MustCompile(`^v(\d+)$`)
	versions := map[int]string{} // version_code -> dir path
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		code, _ := strconv.Atoi(m[1])
		versions[code] = filepath.Join(memRoot, e.Name())
	}
	return versions, nil
}

// isAgentDirectory returns true when absTarget is NOT the memory root itself.
func isAgentDirectory(absTarget, memRoot string) bool {
	return absTarget != memRoot
}

// agentVersion resolves the version for an agent directory.
// Priority: force > metadata.yaml > .memory.lock inference > structural guess.
func agentVersion(agentPath, memRoot string, forceVersion int, available map[int]string) (int, string) {
	if forceVersion > 0 {
		return forceVersion, ""
	}
	// Try metadata.yaml in agent dir.
	metaPath := filepath.Join(agentPath, "metadata.yaml")
	if data, err := os.ReadFile(metaPath); err == nil {
		var m struct {
			VersionCode int `yaml:"version_code"`
		}
		if yaml.Unmarshal(data, &m) == nil && m.VersionCode > 0 {
			return m.VersionCode, ""
		}
	}
	// Try .memory.lock at memory root.
	lockPath := filepath.Join(memRoot, ".memory.lock")
	if data, err := os.ReadFile(lockPath); err == nil {
		agentName := filepath.Base(agentPath)
		var lock map[string]interface{}
		if yaml.Unmarshal(data, &lock) == nil {
			if agents, ok := lock["agents"].(map[string]interface{}); ok {
				if entry, ok := agents[agentName].(map[string]interface{}); ok {
					if vc, ok := entry["version_code"].(int); ok && vc > 0 {
						return vc, ""
					}
				}
			}
			if lv, ok := lock["layout_version"].(int); ok && lv > 0 {
				return lv, ""
			}
		}
	}
	// Structural inference.
	code := inferVersion(agentPath)
	return code, fmt.Sprintf("version not declared in metadata.yaml or .memory.lock; inferred v%d from structure", code)
}

// inferVersion guesses the layout version from directory structure.
func inferVersion(agentPath string) int {
	// v3 marker: skills/ or knowledge/ present.
	if dirExists(filepath.Join(agentPath, "skills")) || dirExists(filepath.Join(agentPath, "knowledge")) {
		return 3
	}
	// v2 marker: gen/ without entry/ wrapper.
	if dirExists(filepath.Join(agentPath, "gen")) && !dirExists(filepath.Join(agentPath, "entry")) {
		return 2
	}
	// v1 marker: entry/ present.
	if dirExists(filepath.Join(agentPath, "entry")) {
		return 1
	}
	return 3 // default to latest
}

// ─── Validate tree ───────────────────────────────────────────────────────────

// resolveAgentByName locates an agent dir by name, supporting both the v3
// layout (<memRoot>/<name>) and the legacy v1/v2 layout (<memRoot>/agents/<name>).
func resolveAgentByName(memRoot, name string) (string, bool) {
	if p := filepath.Join(memRoot, name); dirExists(p) {
		return p, true
	}
	if p := filepath.Join(memRoot, "agents", name); dirExists(p) {
		return p, true
	}
	return "", false
}

// infraDirs are non-agent directories that may sit at the memory root. They
// carry no agent metadata/layout and must not be validated as agents (doing so
// mis-infers them as v3 and reports spurious missing-file errors).
var infraDirs = map[string]bool{
	"templates": true, // version templates (also copied by e2e setup)
	"collab":    true, // v3 cross-agent collaboration
	"circuits":  true, // v3 shared circuits
	"tools":     true, // migrate.sh etc.
}

var versionDirRE = regexp.MustCompile(`^v\d+$`)

func validateTree(targetPath, memRoot string, opts Options, available map[int]string, report Report) (Report, error) {
	entries, err := os.ReadDir(targetPath)
	if err != nil {
		return report, fmt.Errorf("read target %s: %w", targetPath, err)
	}

	rosterRE := regexp.MustCompile(`^(agent_.*\.md|agents_v\d+\.md)$`)
	forbiddenDirs := map[string]bool{"entry": true, "team": true}

	validateAgentInto := func(agentPath string) {
		sub, err := validateSingleAgent(agentPath, memRoot, opts, available)
		if err != nil {
			report.addWarn(agentPath, fmt.Sprintf("skipped: %v", err))
			return
		}
		report.Version = sub.Version // last wins; callers should check per-agent
		report.Violations = append(report.Violations, sub.Violations...)
	}

	for _, e := range entries {
		if !e.IsDir() {
			// Root roster .md check (v3 concern at workspace level).
			if rosterRE.MatchString(e.Name()) {
				report.addWarn(filepath.Join(targetPath, e.Name()),
					"root roster .md file should be removed in v3 (agent dirs are the roster)")
			}
			continue
		}
		name := e.Name()
		// Skip contract dirs, hidden dirs, and known infrastructure dirs —
		// none of these are agents.
		if versionDirRE.MatchString(name) || strings.HasPrefix(name, ".") || infraDirs[name] {
			continue
		}
		// A top-level tasks/ bucket is invalid in v3: own task files belong under
		// an agent (memory/<agent>/tasks/), cross-agent handoffs under collab/tasks/.
		// Consequently the name "tasks" is reserved at the memory root and is never
		// validated as an agent.
		if name == "tasks" {
			report.addWarn(filepath.Join(targetPath, name),
				"top-level memory/tasks/ is not a valid v3 location; own tasks belong at memory/<agent>/tasks/ and handoffs at memory/collab/tasks/<agent>/")
			continue
		}
		// Forbidden dirs at tree root: warn, but do NOT validate as an agent.
		if forbiddenDirs[name] {
			report.addWarn(filepath.Join(targetPath, name),
				fmt.Sprintf("directory %q should not exist in a v3 memory tree", name))
			continue
		}
		// Legacy v1/v2 wrapper: agents/ holds the agent dirs — descend into it
		// and validate each child as an agent (each resolves its own version).
		if name == "agents" {
			agentsDir := filepath.Join(targetPath, name)
			subEntries, err := os.ReadDir(agentsDir)
			if err != nil {
				report.addWarn(agentsDir, fmt.Sprintf("read legacy agents dir: %v", err))
				continue
			}
			for _, se := range subEntries {
				if se.IsDir() {
					validateAgentInto(filepath.Join(agentsDir, se.Name()))
				}
			}
			continue
		}
		// Everything else at root is a v3 agent dir.
		validateAgentInto(filepath.Join(targetPath, name))
	}
	return report, nil
}

// ─── Validate single agent dir ───────────────────────────────────────────────

func validateSingleAgent(agentPath, memRoot string, opts Options, available map[int]string) (Report, error) {
	report := Report{TargetPath: agentPath}

	code, versionWarn := agentVersion(agentPath, memRoot, opts.ForceVersion, available)
	report.Version = code

	if versionWarn != "" {
		report.addWarn(agentPath, versionWarn)
	}

	contractDir, ok := available[code]
	if !ok {
		return report, fmt.Errorf("version v%d contract not found (available: %s)", code, availableVersionString(available))
	}

	// Load contract.
	metaPath := filepath.Join(contractDir, "metadata.yaml")
	semPath := filepath.Join(contractDir, "semantics.yaml")

	if _, err := os.Stat(metaPath); err != nil {
		report.addError(metaPath, "contract metadata.yaml missing")
	}
	if _, err := os.Stat(semPath); err != nil {
		report.addError(semPath, "contract semantics.yaml missing")
		return report, nil
	}

	// Run version-specific structural checks.
	switch code {
	case 3:
		validateV3Agent(agentPath, semPath, opts, &report)
	case 2:
		validateV1V2Agent(agentPath, semPath, 2, &report)
	case 1:
		validateV1V2Agent(agentPath, semPath, 1, &report)
	default:
		report.addWarn(agentPath, fmt.Sprintf("no structural rules implemented for v%d; skipping checks", code))
	}

	// Format checks: all *.yaml in the agent dir must parse.
	lintYAMLUnder(agentPath, &report)

	// Schema checks for the agent's own task files (warnings only).
	lintTaskFilesUnder(agentPath, &report)

	return report, nil
}

// ─── v3 structural checks ─────────────────────────────────────────────────────

func validateV3Agent(agentPath, semPath string, opts Options, report *Report) {
	// Load semantics.
	var sem v3Semantics
	if data, err := os.ReadFile(semPath); err == nil {
		_ = yaml.Unmarshal(data, &sem)
	}

	agentName := filepath.Base(agentPath)

	// Required top-level files.
	for _, f := range sem.Layout.AgentFiles {
		p := filepath.Join(agentPath, f)
		if _, err := os.Stat(p); err != nil {
			if f == "memory.md" && opts.Fix {
				scaffoldMemoryMd(p, agentName, report)
			} else {
				report.addError(p, fmt.Sprintf("required file %q missing", f))
			}
		} else {
			fixAgentNamePlaceholder(p, agentName, opts.Fix, report)
		}
	}

	// Required dirs (and their required files/subdirs).
	for _, d := range sem.Layout.AgentDirs {
		validateV3Dir(agentPath, d, opts, report)
	}

	// Absent-in-v3 dirs.
	for _, forbidden := range sem.Layout.AbsentInV3 {
		fp := filepath.Join(agentPath, forbidden)
		if dirExists(fp) {
			report.addError(fp, fmt.Sprintf("directory %q must not exist in v3 layout", forbidden))
		}
	}

	// Circuit rule: every folder must have a memory.md.
	enforceCircuitRule(agentPath, opts, report)
}

func validateV3Dir(base string, d v3AgentDir, opts Options, report *Report) {
	dirPath := filepath.Join(base, d.Name)
	if !dirExists(dirPath) {
		report.addError(dirPath, fmt.Sprintf("required directory %q missing", d.Name))
		return
	}
	// Required files within the dir.
	for _, f := range d.RequiredFiles {
		p := filepath.Join(dirPath, f)
		if _, err := os.Stat(p); err != nil {
			if f == "memory.md" && opts.Fix {
				scaffoldMemoryMd(p, d.Name, report)
			} else {
				report.addError(p, fmt.Sprintf("required file %q missing in %s", f, d.Name))
			}
		}
	}
	// Required subdirs.
	for _, sub := range d.RequiredSubdirs {
		validateV3Dir(dirPath, sub, opts, report)
	}
}

// enforceCircuitRule checks that every directory under agentPath has a memory.md.
// It runs after the structural checks (validateV3Dir), which already flag missing
// required memory.md files by their full path; we skip those paths here to avoid
// double-reporting the same missing file.
func enforceCircuitRule(agentPath string, opts Options, report *Report) {
	agentName := filepath.Base(agentPath)
	alreadyReported := make(map[string]bool)
	for _, v := range report.Violations {
		alreadyReported[v.Path] = true
	}
	err := filepath.Walk(agentPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		mmd := filepath.Join(path, "memory.md")
		if alreadyReported[mmd] {
			return nil
		}
		if _, err := os.Stat(mmd); err != nil {
			if opts.Fix {
				scaffoldMemoryMd(mmd, filepath.Base(path), report)
			} else {
				report.addError(mmd, fmt.Sprintf("circuit rule: directory %q has no memory.md", filepath.Base(path)))
			}
		} else {
			fixAgentNamePlaceholder(mmd, agentName, opts.Fix, report)
		}
		return nil
	})
	if err != nil {
		report.addWarn(agentPath, fmt.Sprintf("circuit rule walk error: %v", err))
	}
}

// fixAgentNamePlaceholder checks a file for literal "<agent_name>" placeholders.
// When fix is true it replaces them in-place and records an Info violation.
// When fix is false it records a Warning violation.
func fixAgentNamePlaceholder(path, agentName string, fix bool, report *Report) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	s := string(data)
	if !strings.Contains(s, "<agent_name>") {
		return
	}
	if fix {
		replaced := strings.ReplaceAll(s, "<agent_name>", agentName)
		if err := os.WriteFile(path, []byte(replaced), 0o644); err != nil {
			report.addWarn(path, fmt.Sprintf("fix: replace <agent_name>: write failed: %v", err))
			return
		}
		report.Violations = append(report.Violations, Violation{
			Path:     path,
			Severity: SeverityInfo,
			Message:  "fix: replaced literal <agent_name> with " + agentName,
		})
	} else {
		report.addWarn(path, "file contains literal <agent_name> placeholder (run with --fix to repair)")
	}
}

// scaffoldMemoryMd creates a memory.md with proper v3 circuit frontmatter.
func scaffoldMemoryMd(path, name string, report *Report) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		report.addWarn(path, fmt.Sprintf("fix: cannot create parent dir: %v", err))
		return
	}
	// Capitalise the name for the heading (first letter upper-case).
	heading := name
	if len(name) > 0 {
		heading = strings.ToUpper(name[:1]) + name[1:]
	}
	content := fmt.Sprintf("---\ncircuit: %s\nversion: v3\nsummary:\n---\n# %s circuit\n", name, heading)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		report.addWarn(path, fmt.Sprintf("fix: cannot write memory.md: %v", err))
		return
	}
	report.Violations = append(report.Violations, Violation{
		Path:     path,
		Severity: SeverityInfo,
		Message:  "fix: scaffolded missing memory.md",
	})
}

// ─── v1/v2 structural checks ─────────────────────────────────────────────────

func validateV1V2Agent(agentPath, semPath string, version int, report *Report) {
	var sem v1v2Semantics
	if data, err := os.ReadFile(semPath); err == nil {
		_ = yaml.Unmarshal(data, &sem)
	}

	requiredDirs := sem.Layout.AgentDirs
	if len(requiredDirs) == 0 {
		// Fallback hard-coded sets so the validator works even with a parse issue.
		switch version {
		case 1:
			requiredDirs = []string{"entry/instructions/", "entry/tasks/", "generated/", "state/"}
		case 2:
			requiredDirs = []string{"instructions/", "tasks/", "gen/", "state/"}
		}
	}

	for _, rawDir := range requiredDirs {
		// Strip trailing comments (# …) if any.
		dir := strings.TrimSpace(strings.SplitN(rawDir, "#", 2)[0])
		if dir == "" {
			continue
		}
		// Skip dirs that are optional in v1/v2. The "(optional)" hint in
		// semantics.yaml is a YAML comment that is stripped on unmarshal, so we
		// can't detect it from the parsed value — match the known optional paths
		// explicitly. The data dir is optional and lives at entry/data/ in v1 and
		// data/ in v2; sandbox/ is optional in both.
		switch dir {
		case "data/", "entry/data/", "sandbox/":
			continue
		}
		p := filepath.Join(agentPath, filepath.FromSlash(dir))
		if !dirExists(p) {
			report.addError(p, fmt.Sprintf("v%d required directory %q missing", version, dir))
		}
	}
}

// ─── Format / lint helpers ───────────────────────────────────────────────────

// agentContentDirs are subdirectory names that hold agent-authored content files
// (instructions, tasks, knowledge) which may use the operator's custom YAML-like
// tag syntax rather than strict YAML.  YAML parse failures in these dirs are
// downgraded to warnings so pre-existing custom-format files do not block v3
// validation.
var agentContentDirs = map[string]bool{
	"instructions": true,
	"tasks":        true,
	"knowledge":    true,
}

// lintYAMLUnder checks that every *.yaml file under root parses cleanly.
// Files inside agent content directories (instructions/, tasks/) that fail
// YAML parsing are reported as warnings, not errors, because the operator
// uses a custom YAML-like syntax in those files that predates the strict parser.
func lintYAMLUnder(root string, report *Report) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			if lintErr := lintYAMLFile(path); lintErr != nil {
				// Determine the top-level subdirectory relative to root.
				rel := strings.TrimPrefix(path, root+string(os.PathSeparator))
				topDir := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
				if agentContentDirs[topDir] {
					report.addWarn(path, lintErr.Error())
				} else {
					report.addError(path, lintErr.Error())
				}
			}
		}
		return nil
	})
}

// lintTaskFilesUnder applies light schema checks to the agent's own task files
// under <agentPath>/tasks/.  These are warnings, not errors, because the operator's
// task files use a custom YAML-like syntax that predates strict schemas; the goal is
// to nudge the loose, drifted shapes toward a consistent one.
func lintTaskFilesUnder(agentPath string, report *Report) {
	tasksRoot := filepath.Join(agentPath, "tasks")
	if !dirExists(tasksRoot) {
		return
	}
	_ = filepath.Walk(tasksRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		// Placement: task files must live under a namespace sub-circuit
		// (tasks/<namespace>/<name>.yaml), never directly in tasks/. A file whose
		// path relative to tasksRoot has no separator is non-namespaced — reject it.
		if rel, relErr := filepath.Rel(tasksRoot, path); relErr == nil &&
			!strings.Contains(rel, string(os.PathSeparator)) {
			report.addError(path, "task file must live under a namespace dir (tasks/<namespace>/<name>.yaml), not directly in tasks/")
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		text := string(data)
		// `instructions :` with a space before the colon (malformed key).
		if strings.Contains(text, "instructions :") {
			report.addWarn(path, "task schema: 'instructions :' has a space before the colon; use 'instructions:'")
		}
		// `task:` checks: warn on a bare/empty task value, and on a file that
		// declares no task entry at all.
		hasTaskKey, hasEmptyTask := false, false
		for _, line := range strings.Split(text, "\n") {
			t := strings.TrimPrefix(strings.TrimSpace(line), "- ")
			switch {
			case t == "task:" || t == "task :":
				hasTaskKey, hasEmptyTask = true, true
			case strings.HasPrefix(t, "task:") || strings.HasPrefix(t, "task :"):
				hasTaskKey = true
			}
		}
		if hasEmptyTask {
			report.addWarn(path, "task schema: 'task:' has an empty value; give the task a name or file")
		}
		if !hasTaskKey {
			report.addWarn(path, "task schema: no 'task:' entry found; a task file must declare at least one task")
		}
		// Missing recommended top-level keys.
		for _, key := range []string{"type", "version", "status"} {
			if !taskHasKey(text, key) {
				report.addWarn(path, fmt.Sprintf("task schema: missing recommended '%s:' field", key))
			}
		}
		return nil
	})
}

// taskHasKey reports whether text declares the given top-level-ish key, tolerating
// a leading list dash and an errant space before the colon.
func taskHasKey(text, key string) bool {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimPrefix(strings.TrimSpace(line), "- ")
		if strings.HasPrefix(t, key+":") || strings.HasPrefix(t, key+" :") {
			return true
		}
	}
	return false
}

func lintYAMLFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	var out interface{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("yaml parse: %w", err)
	}
	return nil
}

// ─── Utility ─────────────────────────────────────────────────────────────────

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func availableVersionString(available map[int]string) string {
	var parts []string
	for code := range available {
		parts = append(parts, fmt.Sprintf("v%d", code))
	}
	return strings.Join(parts, ", ")
}
