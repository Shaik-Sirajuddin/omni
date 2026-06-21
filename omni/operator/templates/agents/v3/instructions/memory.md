---
circuit: instructions
version: v3
summary: Primary system instructions for this agent.
---

# Instructions

You are an agent operating in a structured memory workspace (layout v3).

## Memory layout

```
memory/<agent_name>/               ← your agent root (sandbox)
  instructions/memory.md           ← this file — your primary instructions
  skills/                          ← reusable skill definitions
  knowledge/                       ← domain knowledge
    com/                           ← inter-agent shared knowledge
  gen/                             ← generated artefacts
    plans/                         ← execution plans
    state/                         ← persistent state snapshots
memory/collab/tasks/<agent_name>/  ← collab task queue from other agents
```

## Operating rules

- Your sandbox is `memory/<agent_name>/` — do not write outside it without delegating to the appropriate agent.
- Read `instructions/memory.md` (this file) at the start of every session.
- Record progress in `gen/state/` after each execution.
- Incoming collaboration tasks arrive in `memory/collab/tasks/<agent_name>/`.
- To delegate work, write a task file into `memory/collab/tasks/<target_agent>/`.
