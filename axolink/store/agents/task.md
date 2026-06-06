# Agents — Deferred to Omni

Agent reads are handled directly via the omni module:
- `GetReadOnlyOperatorStore()` → `store/operator` (omni)
- `ListAgentsByDir(workspaceDir)` → lists agents for a workspace
- `GetAgent(id)` → fetch by ID

Callers should import `github.com/Shaik-Sirajuddin/memory/store/operator` directly.
No wrapper needed in this package.
