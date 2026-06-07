package internal

import (
	"fmt"
	"os"
	"time"
)

func watchTerminal(t *PTYTerminal, store *Store, onExit func(agentID, sessionID string)) {
	err := t.cmd.Wait()

	status := StatusStopped
	if err != nil {
		status = StatusCrashed
	}

	t.setStatus(status)
	// Snapshot the identity under t.mu: Adopt may have re-keyed AgentID from ""
	// to the real id on this same object, so an unlocked read here would race it.
	t.mu.Lock()
	agentID, sessionID := t.AgentID, t.SessionID
	t.mu.Unlock()
	_ = store.UpdateStatus(agentID, sessionID, status)
	t.closeMaster() // child is gone; release the master fd now instead of at GC

	if onExit != nil {
		onExit(agentID, sessionID)
	}
}

func watchAdopted(t *PTYTerminal, store *Store, remove func(string, string)) {
	procDir := fmt.Sprintf("/proc/%d", t.PID)
	for {
		if _, err := os.Stat(procDir); os.IsNotExist(err) {
			t.setStatus(StatusStopped)
			_ = store.UpdateStatus(t.AgentID, t.SessionID, StatusStopped)
			t.closeMaster() // adopted process gone; release the opened master fd
			remove(t.AgentID, t.SessionID)
			return
		}
		time.Sleep(2 * time.Second)
	}
}
