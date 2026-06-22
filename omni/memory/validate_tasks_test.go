package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v3 contract used by these tests. Mirrors memory/v3/semantics.yaml's relevant
// sections (agent layout incl. the first-class tasks/ dir, and absent_in_v3).
const testV3Semantics = `
layout:
  agent_root: "memory/<agent_name>/"
  agent_files:
    - memory.md
    - metadata.yaml
  agent_dirs:
    - name: instructions
      required_files: [memory.md]
    - name: skills
      required_files: [memory.md]
    - name: tasks
      required_files: [memory.md]
    - name: knowledge
      required_files: [memory.md]
      required_subdirs:
        - name: com
          required_files: [memory.md]
    - name: gen
      required_files: [memory.md]
      required_subdirs:
        - name: plans
          required_files: [memory.md]
        - name: state
          required_files: [memory.md]
  absent_in_v3:
    - entry
    - team
    - agents
    - generated
`

const testV3Metadata = "version: v3\nversion_code: 3\nstatus: current\n"

func writeMD(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\ncircuit: x\nversion: v3\nsummary:\n---\n# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newV3Root creates a temp memory root containing the v3 contract.
func newV3Root(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "v3", "semantics.yaml"), testV3Semantics)
	writeFileT(t, filepath.Join(root, "v3", "metadata.yaml"), testV3Metadata)
	return root
}

// scaffoldV3Agent writes a fully-valid v3 agent (all required dirs + circuit
// memory.md files) at <root>/<name> and returns its path.
func scaffoldV3Agent(t *testing.T, root, name string) string {
	t.Helper()
	a := filepath.Join(root, name)
	writeMD(t, filepath.Join(a, "memory.md"))
	writeFileT(t, filepath.Join(a, "metadata.yaml"), "version_code: 3\n")
	for _, d := range []string{
		"instructions", "skills", "tasks", "knowledge",
		filepath.Join("knowledge", "com"),
		"gen", filepath.Join("gen", "plans"), filepath.Join("gen", "state"),
	} {
		writeMD(t, filepath.Join(a, d, "memory.md"))
	}
	return a
}

func errorMessages(r Report) string {
	var b strings.Builder
	for _, v := range r.Violations {
		b.WriteString(string(v.Severity) + ": " + v.Message + "\n")
	}
	return b.String()
}

func hasMsg(r Report, sev Severity, substr string) bool {
	for _, v := range r.Violations {
		if v.Severity == sev && strings.Contains(v.Message, substr) {
			return true
		}
	}
	return false
}

// A v3 agent with a well-formed tasks/<ns>/<name>.yaml validates clean.
func TestV3TasksDirAcceptedWithNamespace(t *testing.T) {
	root := newV3Root(t)
	a := scaffoldV3Agent(t, root, "alpha")
	writeMD(t, filepath.Join(a, "tasks", "v1.2.8", "memory.md"))
	writeFileT(t, filepath.Join(a, "tasks", "v1.2.8", "agent.yaml"),
		"tasks:\n- task: do-thing\n  type: impl\n  version: v1.2.8\n  status: open\n  instructions:\n  - go\n")

	rep, err := Validate(a, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if rep.HasViolations() {
		t.Fatalf("expected no errors, got:\n%s", errorMessages(rep))
	}
}

// A v3 agent missing tasks/ is rejected (tasks is now required).
func TestV3MissingTasksDirRejected(t *testing.T) {
	root := newV3Root(t)
	a := scaffoldV3Agent(t, root, "beta")
	os.RemoveAll(filepath.Join(a, "tasks"))

	rep, err := Validate(a, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasMsg(rep, SeverityError, `required directory "tasks" missing`) {
		t.Fatalf("expected missing-tasks error, got:\n%s", errorMessages(rep))
	}
}

// A v3 agent using the legacy entry/tasks/ shape is rejected (entry is absent_in_v3).
func TestV3LegacyEntryTasksRejected(t *testing.T) {
	root := newV3Root(t)
	a := scaffoldV3Agent(t, root, "gamma")
	writeMD(t, filepath.Join(a, "entry", "tasks", "memory.md"))

	rep, err := Validate(a, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasMsg(rep, SeverityError, `"entry" must not exist`) {
		t.Fatalf("expected entry-forbidden error, got:\n%s", errorMessages(rep))
	}
}

// A top-level memory/tasks/ bucket is flagged in whole-tree validation.
func TestTopLevelTasksBucketFlagged(t *testing.T) {
	root := newV3Root(t)
	scaffoldV3Agent(t, root, "delta")
	writeMD(t, filepath.Join(root, "tasks", "memory.md")) // invalid top-level bucket

	rep, err := Validate(root, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasMsg(rep, SeverityWarning, "top-level memory/tasks/ is not a valid v3 location") {
		t.Fatalf("expected top-level tasks warning, got:\n%s", errorMessages(rep))
	}
}

// Loose task schemas produce warnings (empty task:, malformed spacing, missing fields).
func TestTaskSchemaLintWarnings(t *testing.T) {
	root := newV3Root(t)
	a := scaffoldV3Agent(t, root, "epsilon")
	writeMD(t, filepath.Join(a, "tasks", "v0", "memory.md"))
	writeFileT(t, filepath.Join(a, "tasks", "v0", "loose.yaml"),
		"tasks:\n- task: \n  instructions :\n  - do something\n")

	rep, err := Validate(a, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, want := range []string{
		"'task:' has an empty value",
		"'instructions :' has a space before the colon",
		"missing recommended 'type:' field",
		"missing recommended 'status:' field",
	} {
		if !hasMsg(rep, SeverityWarning, want) {
			t.Fatalf("expected warning %q, got:\n%s", want, errorMessages(rep))
		}
	}
}

func countPath(r Report, path string) int {
	n := 0
	for _, v := range r.Violations {
		if v.Path == path {
			n++
		}
	}
	return n
}

// A missing required memory.md is reported exactly once (no double-report from
// the structural check + circuit rule overlap).
func TestMissingMemoryMdReportedOnce(t *testing.T) {
	root := newV3Root(t)
	a := scaffoldV3Agent(t, root, "zeta")
	mmd := filepath.Join(a, "skills", "memory.md")
	os.Remove(mmd)

	rep, err := Validate(a, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := countPath(rep, mmd); got != 1 {
		t.Fatalf("expected exactly 1 violation for %s, got %d:\n%s", mmd, got, errorMessages(rep))
	}
}

// The circuit rule enforces memory.md inside a tasks/<namespace>/ subdir, and
// reports it exactly once.
func TestTasksNamespaceMissingMemoryMd(t *testing.T) {
	root := newV3Root(t)
	a := scaffoldV3Agent(t, root, "eta")
	// Create a namespace dir with a task file but no memory.md.
	writeFileT(t, filepath.Join(a, "tasks", "v1", "x.yaml"),
		"tasks:\n- task: t\n  type: impl\n  version: v1\n  status: open\n")
	mmd := filepath.Join(a, "tasks", "v1", "memory.md")

	rep, err := Validate(a, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasMsg(rep, SeverityError, "circuit rule") {
		t.Fatalf("expected circuit-rule error for missing namespace memory.md, got:\n%s", errorMessages(rep))
	}
	if got := countPath(rep, mmd); got != 1 {
		t.Fatalf("expected exactly 1 violation for %s, got %d:\n%s", mmd, got, errorMessages(rep))
	}
}

// A task file placed directly under tasks/ (no namespace dir) is rejected.
func TestTaskFileMustBeNamespaced(t *testing.T) {
	root := newV3Root(t)
	a := scaffoldV3Agent(t, root, "kappa")
	// Well-formed schema, but placed directly in tasks/ instead of tasks/<ns>/.
	writeFileT(t, filepath.Join(a, "tasks", "foo.yaml"),
		"tasks:\n- task: do-thing\n  type: impl\n  version: v1\n  status: open\n")

	rep, err := Validate(a, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasMsg(rep, SeverityError, "must live under a namespace dir") {
		t.Fatalf("expected non-namespaced task-file error, got:\n%s", errorMessages(rep))
	}
}

// A task file with no task: entry at all is flagged.
func TestTaskSchemaMissingTaskEntry(t *testing.T) {
	root := newV3Root(t)
	a := scaffoldV3Agent(t, root, "theta")
	writeMD(t, filepath.Join(a, "tasks", "v0", "memory.md"))
	writeFileT(t, filepath.Join(a, "tasks", "v0", "notes.yaml"),
		"type: impl\nversion: v0\nstatus: open\n")

	rep, err := Validate(a, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasMsg(rep, SeverityWarning, "no 'task:' entry found") {
		t.Fatalf("expected missing-task warning, got:\n%s", errorMessages(rep))
	}
}

// A well-formed task file produces no task-schema warnings.
func TestTaskSchemaCleanFileNoWarnings(t *testing.T) {
	root := newV3Root(t)
	a := scaffoldV3Agent(t, root, "iota")
	writeMD(t, filepath.Join(a, "tasks", "v1", "memory.md"))
	writeFileT(t, filepath.Join(a, "tasks", "v1", "ok.yaml"),
		"tasks:\n- task: do-thing\n  type: impl\n  version: v1\n  status: open\n  instructions:\n  - go\n")

	rep, err := Validate(a, Options{MemoryRoot: root})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, bad := range []string{"task schema:"} {
		if hasMsg(rep, SeverityWarning, bad) {
			t.Fatalf("expected no task-schema warnings on a clean file, got:\n%s", errorMessages(rep))
		}
	}
}

// Guard: the shipped contract (memory/v3/semantics.yaml) keeps tasks as a v3 agent dir.
func TestShippedContractDeclaresTasks(t *testing.T) {
	// omni/memory -> omni -> repo root -> memory/v3/semantics.yaml
	p := filepath.Join("..", "..", "memory", "v3", "semantics.yaml")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("shipped contract not available: %v", err)
	}
	if !strings.Contains(string(data), "name: tasks") {
		t.Fatalf("memory/v3/semantics.yaml must declare a 'tasks' agent dir")
	}
}
