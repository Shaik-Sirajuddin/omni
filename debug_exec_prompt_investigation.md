# Investigation: ExecInSession prompt pasted but not submitted

## Log file: .dev/logs/resume-logs.txt
## Command: `omni agent exec config --prompt "hello , theere " --resume`

---

## Exact sequence (from log timestamps)

```
10:27:56.187  ptyDaemon.List(agent) count=0           ← CLI/ResumeAgent pre-check
10:27:56.189  MetaAttached(session) count=0           ← ResumeAgent early-exit check (line 623)
10:27:56.261  ptyDaemon.Start ok                      ← PTY process launched
10:27:56.262  registerPTYSession skipped              ← result.ProcessID == ""  (line 503)
10:27:56.262  ResumeAgent: completed                  ← first ResumeAgent done

10:27:56.327  codeAgentForAgentID (ExecInSession re-init, line 281)
10:27:56.328  MetaAttached count=0                    ← ExecInSession active check (line 303)
10:27:56.328  ExecInSession: session not active, auto-resuming  (line 308)
10:27:56.391  Resume: reusing active PTY session (status=active)
10:27:56.391  registerPTYSession skipped again        ← same reason
10:27:56.391  second ResumeAgent: completed

10:27:56.448  ptyDaemon.Exec input_len=15             ← exec fires 187ms after first PTY start
10:27:56.631  ExecInSession: prompt delegated         ← connector delivers prompt
```

---

## Answers to investigation questions

### 1. Why is "process id is empty" after PTY start?

`registerPTYSession` (line 498) skips when `result.ProcessID == ""`.
The PTY daemon DID start the process (confirmed by `start ok` and later `status=active`).
The claude connector's Resume path calls `ptyDaemon.Start` and returns a `ResumeSessionResult`
with `SessionID` set but `ProcessID` left empty — it does not pass the PID from the daemon's
start response back into the result struct.

This is a connector-side omission, not an operator bug.

### 2. Does "pty daemon registration skipped" affect exec targeting?

Yes, directly. Without `ptyDaemon.Register(agentID, sessionID, PID)` being called, the daemon
has no "meta-attachment" record for this session. `MetaAttached(sessionID)` therefore returns 0
even while the PTY process is running and active.

ExecInSession checks `MetaAttached` to decide if the session is live (line 302-306). It returns
0 → `sessionActive = false` → the auto-resume branch fires redundantly even though the PTY
was already started by the prior `--resume` path.

### 3. Is there a timing issue between PTY start and exec?

Yes — this is the proximate cause of the "pasted not submitted" symptom.

Exec fires at `10:27:56.448` which is only **187ms** after the PTY process started
(`10:27:56.261`). Claude Code's TUI needs time to initialise its readline/input loop and
display the `>` prompt. When the exec payload arrives that early, the keystrokes land in the
terminal's input buffer before Claude is listening — they appear as pasted raw text with no
submit being processed.

The second ResumeAgent itself returns almost instantly (session was already active, just
reused it), so there is zero deliberate wait between "resume returned" and "exec fires".

### 4. Is the auto-resume path sequenced correctly?

No — two sequencing problems:

**Problem A — duplicate resume:** CLI calls `ResumeAgent` (via `--resume` flag) then calls
`ExecInSession`. `ExecInSession` doesn't know about the prior resume because `MetaAttached`
returns 0 (registration was skipped). It fires a second `ResumeAgent`, adding ~66ms overhead
and noise.

**Problem B — no readiness wait:** After auto-resume in `ExecInSession` (line 308-327), the
code immediately re-inits `ca` and calls `ca.ExecInSession` with no grace period. There is no
mechanism to wait for Claude's TUI to be ready to accept input.

---

## Root cause summary

Two chained failures:

1. **claude connector** doesn't populate `result.ProcessID` → `registerPTYSession` skips →
   `MetaAttached` always returns 0 → duplicate resume triggered.

2. **operator ExecInSession** fires exec immediately after resume returns with no startup
   grace period → 187ms race against Claude TUI init → prompt lands as raw paste.

---

## Fix options (operator-scope)

### Option A — Startup grace period after auto-resume (operator-only, minimal)
After the auto-resume block in `ExecInSession` (after line 327), sleep a configurable duration
(e.g., `500ms`) before calling `ca.ExecInSession`. Eliminates the timing race.

**Pro:** Simple, one-line change. Works regardless of registration.
**Con:** Fixed sleep — too short on slow machines, unnecessarily long on fast ones.

### Option B — Poll PTY status + grace period (operator-only, robust)
After auto-resume, poll `ptyDaemon.GetSession(sessionID)` until `status == "active"` (already
confirmed active at the time of exec in the log), then add a smaller grace period (200-300ms).
`GetSession` already exists in the daemon client.

**Pro:** Adapts to actual PTY state; grace period is shorter.
**Con:** Slightly more complex; still relies on a time-based heuristic for TUI readiness.

### Option C — Fallback active-session check to avoid duplicate resume (operator-only)
In `ExecInSession`, when `MetaAttached` returns 0, additionally call
`ptyDaemon.GetSession(sessionID)` — if `status == "active"`, treat session as live (skip
auto-resume). Combine with Option A/B grace period.

**Pro:** Eliminates the duplicate resume; reduces total elapsed time.
**Con:** Doesn't fix the ProcessID root cause (still a connector issue).

### Option D — Fix ProcessID in claude connector (cross-sandbox, cleanest)
Claude connector populates `result.ProcessID` from the daemon's start response.
`registerPTYSession` fires → `MetaAttached` returns 1 → no duplicate resume → `ExecInSession`
proceeds directly without auto-resume on the second call.
Still needs a readiness grace period (Option A/B) for the first-resume case.

**Recommendation:** Option D (connector fix, reported to claude-connector agent) +
Option B (operator grace period) gives the correct long-term fix with no timing races.
Short-term operator-only fix: Option C + Option A.
