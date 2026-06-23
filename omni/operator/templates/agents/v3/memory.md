---
circuit: root
version: v3
summary: Agent root index for <agent_name>.
---

# Agent: <agent_name>

This is the memory root for agent `<agent_name>` (layout v3).

## Quick reference

| Path | Purpose |
|------|---------|
| `instructions/memory.md` | Primary system instructions — read at session start |
| `skills/` | Reusable skill definitions |
| `tasks/` | Your own task definitions, flat and version-namespaced (`tasks/<ns>/<name>.yaml`) |
| `knowledge/` | Domain knowledge; `com/` for inter-agent shared facts |
| `gen/plans/` | Execution plans |
| `gen/state/` | State snapshots after each execution |
| `memory/collab/tasks/<agent_name>/` | Incoming collaboration tasks |
