version: 0.0.1
details:
- ptydaemon drains PTY output while a session is detached (no fd-passing client attached), so a child that out-produces the kernel PTY buffer no longer blocks on write — `create → exec` now runs to completion fully headless.
- 0-buffer design: drained output is discarded (no scrollback retained). On attach the child is asked to repaint via a TIOCSWINSZ winsize-nudge (raises SIGWINCH), so full-screen TUIs (claude, codex) redraw the current screen; line-oriented programs are unaffected.
- drain pauses while a client holds the master fd (one reader at a time) and resumes on detach.
- PTY master fd is now closed eagerly on child exit / stop / adopted-process exit instead of lingering until GC.
- adopted sessions do not drain (their master fd is externally owned); repaint still applies.
- known limits: brief read-handoff window on attach (covered by repaint); a client that dies without sending detach can leave the drain paused until re-attach/detach (still strictly better than the previous always-stall).

changed: svc/ptydaemon — idle output drain + repaint-on-attach + eager master fd close

interfaces:
- svc/ptydaemon/ptyunix/daemon.go : attachAware (optional OnAttach/OnDetach, duck-typed)

functions:
- svc/ptydaemon/internal/terminal.go > closeMaster
- svc/ptydaemon/internal/terminal.go > startDrain
- svc/ptydaemon/internal/terminal.go > drainLoop
- svc/ptydaemon/internal/terminal.go > pauseDrain
- svc/ptydaemon/internal/terminal.go > resumeDrain
- svc/ptydaemon/internal/terminal.go > stopDrain
- svc/ptydaemon/internal/terminal.go > repaint
- svc/ptydaemon/internal/daemon.go > defaultDaemon.OnAttach
- svc/ptydaemon/internal/daemon.go > defaultDaemon.OnDetach
- svc/ptydaemon/internal/daemon.go > defaultDaemon.Create (startDrain wiring)
- svc/ptydaemon/internal/watcher.go > watchTerminal / watchAdopted (closeMaster wiring)
- svc/ptydaemon/ptyunix/daemon.go > handleAttach (OnAttach wiring)
- svc/ptydaemon/ptyunix/daemon.go > handleDetach (OnDetach wiring)

structs:
- svc/ptydaemon/internal/terminal.go : PTYTerminal (added drainMu, drainCond, hasDrain, drainActive, drainClosed)
