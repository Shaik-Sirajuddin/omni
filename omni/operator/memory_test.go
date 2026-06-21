package operator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// latestEmbeddedVersion
// ---------------------------------------------------------------------------

func TestLatestEmbeddedVersion(t *testing.T) {
	v := latestEmbeddedVersion()
	assert.Equal(t, "v3", v, "latestEmbeddedVersion should return v3 (highest embedded template)")
}

// ---------------------------------------------------------------------------
// defaultVersion
// ---------------------------------------------------------------------------

func TestDefaultVersion(t *testing.T) {
	t.Run("FallsBackToLatestEmbeddedWhenNoVersionYaml", func(t *testing.T) {
		dir := t.TempDir()
		got := defaultVersion(dir)
		assert.Equal(t, latestEmbeddedVersion(), got, "defaultVersion should return latestEmbeddedVersion when version.yaml is absent")
	})

	t.Run("ReadsDefaultVersionFromVersionYaml", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, versionConfigFile), []byte("default_version: v1\n"), 0o644))
		got := defaultVersion(dir)
		assert.Equal(t, "v1", got, "defaultVersion should honour default_version in version.yaml")
	})

	t.Run("StripsInlineYAMLComments", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, versionConfigFile), []byte("default_version: v1 # pinned\n"), 0o644))
		got := defaultVersion(dir)
		assert.Equal(t, "v1", got, "defaultVersion should strip inline YAML comments")
	})

	t.Run("FallsBackWhenDefaultVersionValueIsEmpty", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, versionConfigFile), []byte("default_version:\n"), 0o644))
		got := defaultVersion(dir)
		assert.Equal(t, latestEmbeddedVersion(), got, "defaultVersion should fall back when the value is empty")
	})
}

// ---------------------------------------------------------------------------
// writeVersionConfig
// ---------------------------------------------------------------------------

func TestWriteVersionConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeVersionConfig(dir, "v3"))

	data, err := os.ReadFile(filepath.Join(dir, versionConfigFile))
	require.NoError(t, err, "version.yaml should be written")
	assert.Contains(t, string(data), "default_version: v3", "version.yaml should contain default_version: v3")
}

// ---------------------------------------------------------------------------
// Create / applyTemplate — v3 layout
// ---------------------------------------------------------------------------

func TestDefaultAgentMemoryCreateV3(t *testing.T) {
	// Set up a fake workspace root with version.yaml pointing to v3 (the default).
	workspaceRoot := t.TempDir()
	require.NoError(t, writeVersionConfig(workspaceRoot, "v3"))

	mem := NewDefaultAgentMemory()
	agentName := "alpha"
	// In v3, agent lives directly under memRoot/<agentName>.
	memDir := filepath.Join(workspaceRoot, agentName)

	err := mem.Create(memDir)
	require.NoError(t, err, "Create should succeed with v3 layout")

	t.Run("RequiredDirsExist", func(t *testing.T) {
		assert.DirExists(t, filepath.Join(memDir, "instructions"), "instructions/ must exist")
		assert.DirExists(t, filepath.Join(memDir, "skills"), "skills/ must exist")
		assert.DirExists(t, filepath.Join(memDir, "knowledge", "com"), "knowledge/com must exist")
		assert.DirExists(t, filepath.Join(memDir, "gen", "plans"), "gen/plans must exist")
		assert.DirExists(t, filepath.Join(memDir, "gen", "state"), "gen/state must exist")
	})

	t.Run("MetadataYamlHasVersionCode3", func(t *testing.T) {
		code, err := readVersionCode(memDir)
		require.NoError(t, err, "readVersionCode should succeed")
		assert.Equal(t, 3, code, "metadata.yaml version_code should be 3")
	})

	t.Run("InstructionsMemoryMdExists", func(t *testing.T) {
		assert.FileExists(t, filepath.Join(memDir, "instructions", "memory.md"), "instructions/memory.md must be seeded")
	})

	t.Run("CollabTasksDirCreated", func(t *testing.T) {
		assert.FileExists(t, filepath.Join(workspaceRoot, "collab", "tasks", agentName, "default.yaml"),
			"collab/tasks/<agent>/default.yaml must be seeded (v3)")
	})

	t.Run("NoAgentsSubDir", func(t *testing.T) {
		_, err := os.Stat(filepath.Join(workspaceRoot, "agents", agentName))
		assert.ErrorIs(t, err, os.ErrNotExist, "v3 must NOT create agents/ sub-directory")
	})

	t.Run("NoWorkspaceAgentDoc", func(t *testing.T) {
		_, err := os.Stat(filepath.Join(workspaceRoot, "agent_"+agentName+".md"))
		assert.ErrorIs(t, err, os.ErrNotExist, "v3 must NOT create a per-agent workspace markdown doc")
	})
}

// ---------------------------------------------------------------------------
// Per-workspace version override: version.yaml default_version=v1
// ---------------------------------------------------------------------------

func TestDefaultAgentMemoryCreateRespectsWorkspaceVersionOverride(t *testing.T) {
	// Configure workspace to use v1.
	workspaceRoot := t.TempDir()
	require.NoError(t, writeVersionConfig(workspaceRoot, "v1"))

	mem := NewDefaultAgentMemory()
	agentName := "beta"
	// For v1 the agent lives under agents/<name>.
	memDir := filepath.Join(workspaceRoot, "agents", agentName)

	err := mem.Create(memDir)
	require.NoError(t, err, "Create should succeed with workspace pinned to v1")

	t.Run("V1RequiredDirsExist", func(t *testing.T) {
		assert.DirExists(t, filepath.Join(memDir, "entry", "instructions"), "entry/instructions must exist (v1)")
		assert.DirExists(t, filepath.Join(memDir, "entry", "tasks"), "entry/tasks must exist (v1)")
		assert.DirExists(t, filepath.Join(memDir, "generated"), "generated must exist (v1)")
		assert.DirExists(t, filepath.Join(memDir, "state"), "state must exist (v1)")
	})

	t.Run("MetadataYamlHasVersionCode1or2", func(t *testing.T) {
		// v1 template metadata.yaml has version_code: 2 (a quirk of the existing template).
		code, err := readVersionCode(memDir)
		require.NoError(t, err, "readVersionCode should succeed")
		assert.True(t, code == 1 || code == 2, "metadata.yaml version_code should be 1 or 2 for v1 template (got %d)", code)
	})

	t.Run("WorkspaceDocCreated", func(t *testing.T) {
		assert.FileExists(t, filepath.Join(workspaceRoot, "agent_"+agentName+".md"),
			"v1 should create a per-agent workspace doc")
	})

	t.Run("CollabTasksDirAtLegacyPath", func(t *testing.T) {
		assert.FileExists(t, filepath.Join(workspaceRoot, "team", "entry", "tasks", agentName, "default.yaml"),
			"v1 collab tasks must be at team/entry/tasks/<agent>/default.yaml")
	})
}

// ---------------------------------------------------------------------------
// Init writes version.yaml and seeds workspace
// ---------------------------------------------------------------------------

func TestDefaultAgentMemoryInitWritesVersionYaml(t *testing.T) {
	if _, err := os.LookupEnv("SKIP_GIT_TESTS"); err {
		t.Skip("skipped: SKIP_GIT_TESTS is set")
	}

	workspaceRoot := t.TempDir()
	mem := NewDefaultAgentMemory()

	err := mem.Init(workspaceRoot, "")
	require.NoError(t, err, "Init should succeed")

	memDir := filepath.Join(workspaceRoot, MemoryDirName)

	t.Run("VersionYamlExists", func(t *testing.T) {
		assert.FileExists(t, filepath.Join(memDir, versionConfigFile), "version.yaml must be written by Init")
	})

	t.Run("VersionYamlContainsDefaultVersion", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(memDir, versionConfigFile))
		require.NoError(t, err)
		assert.Contains(t, string(data), "default_version:", "version.yaml must contain default_version key")
	})

	t.Run("DefaultVersionMatchesLatestEmbedded", func(t *testing.T) {
		v := defaultVersion(memDir)
		assert.Equal(t, latestEmbeddedVersion(), v,
			"default_version in version.yaml should match latestEmbeddedVersion when no override was set")
	})
}

// ---------------------------------------------------------------------------
// templateExists — v3 templates are discoverable
// ---------------------------------------------------------------------------

func TestTemplateExistsV3(t *testing.T) {
	assert.True(t, templateExists("v3"), "v3 agent and memory templates should be embedded")
}

func TestTemplateExistsV1(t *testing.T) {
	assert.True(t, templateExists("v1"), "v1 templates should still be embedded")
}

// ---------------------------------------------------------------------------
// ListTemplateVersions — v3 appears in the list
// ---------------------------------------------------------------------------

func TestListTemplateVersionsIncludesV3(t *testing.T) {
	versions, err := ListTemplateVersions()
	require.NoError(t, err, "ListTemplateVersions should not error")
	assert.Contains(t, versions, "v3", "v3 should appear in ListTemplateVersions output")
	assert.Contains(t, versions, "v1", "v1 should still appear in ListTemplateVersions output")
}

// ---------------------------------------------------------------------------
// Upgrade relocates v1→v3
// ---------------------------------------------------------------------------

func TestUpgradeRelocatesV1ToV3(t *testing.T) {
	memRoot := t.TempDir()
	require.NoError(t, writeVersionConfig(memRoot, "v1"))
	agentName := "myagent"
	v1Dir := filepath.Join(memRoot, "agents", agentName)
	require.NoError(t, os.MkdirAll(v1Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(v1Dir, "metadata.yaml"), []byte("version: v1\nversion_code: 1\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(v1Dir, "entry", "instructions"), 0o755))

	mem := NewDefaultAgentMemory()
	newDir, err := mem.Upgrade(v1Dir, "v3")
	require.NoError(t, err)

	expectedDir := filepath.Join(memRoot, agentName)
	assert.Equal(t, expectedDir, newDir, "Upgrade should return the new v3 dir")
	assert.DirExists(t, expectedDir, "v3 dir should exist after relocation")
	_, err = os.Stat(v1Dir)
	assert.True(t, os.IsNotExist(err), "old v1 dir should be gone after relocation")
}

// ---------------------------------------------------------------------------
// Upgrade is idempotent
// ---------------------------------------------------------------------------

func TestUpgradeIdempotent(t *testing.T) {
	memRoot := t.TempDir()
	require.NoError(t, writeVersionConfig(memRoot, "v3"))
	agentName := "myagent"
	v3Dir := filepath.Join(memRoot, agentName)
	require.NoError(t, os.MkdirAll(v3Dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(v3Dir, "metadata.yaml"), []byte("version: v3\nversion_code: 3\n"), 0o644))

	mem := NewDefaultAgentMemory()
	newDir, err := mem.Upgrade(v3Dir, "v3")
	require.NoError(t, err, "Upgrade to same version should be a no-op, not error")
	assert.Equal(t, v3Dir, newDir, "Upgrade no-op should return same dir")
}

// ---------------------------------------------------------------------------
// Created agent has no literal <agent_name> in memory.md
// ---------------------------------------------------------------------------

func TestCreatedAgentHasRealNameInMemoryMd(t *testing.T) {
	memRoot := t.TempDir()
	require.NoError(t, writeVersionConfig(memRoot, "v3"))
	agentName := "specialagent"
	memDir := filepath.Join(memRoot, agentName)

	mem := NewDefaultAgentMemory()
	require.NoError(t, mem.Create(memDir))

	data, err := os.ReadFile(filepath.Join(memDir, "memory.md"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "<agent_name>", "memory.md must not contain literal <agent_name>")
	assert.Contains(t, string(data), agentName, "memory.md must contain the real agent name")

	data2, err := os.ReadFile(filepath.Join(memDir, "instructions", "memory.md"))
	require.NoError(t, err)
	assert.NotContains(t, string(data2), "<agent_name>", "instructions/memory.md must not contain literal <agent_name>")
}
