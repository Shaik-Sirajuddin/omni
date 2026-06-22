---
circuit: tasks
version: v3
summary: This agent's own task definitions.
---

# Tasks

Your own task definitions live here, flat and version-namespaced:

    tasks/<namespace>/<name>.yaml   e.g. tasks/v1.2.8/agent.yaml

Conventions:

- Group task files under a `<namespace>/` sub-circuit (each carries its own `memory.md`).
- Do NOT use `entry/tasks/` (v1/v2 shape) or a top-level `memory/tasks/` bucket.
- Cross-agent handoffs go to `memory/collab/tasks/<target_agent>/`, not here.
- Each task file should set `task:`, `type:`, `version:`, and `status:`.
