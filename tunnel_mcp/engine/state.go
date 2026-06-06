package engine

import "sync"

// TaskKey identifies a task by its ID and the agent that created it.
// Any agent may reference the same task by passing the same pair.
// Empty TaskID means no task grouping — no bypass or priority applies.
type TaskKey struct {
	TaskID         string
	CreatorAgentID string
}

// IsZero reports whether k is unset (empty TaskID).
func (k TaskKey) IsZero() bool {
	return k.TaskID == ""
}

// taskState tracks per-task flags (e.g. paused) within an agent's scope.
type taskState struct {
	Paused bool
}

// Agent identifies an agent by its ID, workspace, and team.
// Team is for filtering only — it is not part of the agent key.
type Agent struct {
	AgentID   string
	Name      string // omni agent name, used when shelling out to omni agent exec
	Workspace string
	Team      string
}

type AgentStatus string

const (
	AgentStatusRunning AgentStatus = "running"
	AgentStatusReady   AgentStatus = "ready"
	AgentStatusStopped AgentStatus = "stopped"
	AgentStatusPaused  AgentStatus = "paused"
)

type StopReason string

const (
	StopReasonTokensExhausted StopReason = "tokens_exhausted"
	StopReasonInterrupted     StopReason = "interrupted"
	StopReasonNetwork         StopReason = "network"
	StopReasonOther           StopReason = "other"
)

type ConsumedUsage struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CachedInput  int64 `json:"cached_input"`
	CachedOutput int64 `json:"cached_output"`
}

type SessionUsage struct {
	Consumed        ConsumedUsage    `json:"consumed"`
	Max             map[string]int64 `json:"max"`
	ConsumedPercent float64          `json:"consumed_percent"`
}

// CodeSession holds the runtime session state of an agent's coding loop.
type CodeSession struct {
	IsInterrupted     bool
	SessionID         string
	SessionGeneration int // incremented by markDelivered; each executeLoop goroutine captures its value before ExecInSession to guard onSessionEnd from resetting a newer session's Running state.
}

type AgentState struct {
	Agent        Agent
	Status       AgentStatus
	SessionUsage SessionUsage
	StopReason   StopReason
	CodeSession  CodeSession
}

// EngineState holds the in-memory view of all agent and delivery state.
type EngineState struct {
	mu           sync.RWMutex
	agents       map[string]*AgentState
	pending      map[string]bool              // agentID → has undelivered messages
	sessions     map[string]string            // sessionID → agentID
	taskMux      map[string]*TaskKey          // agentID → active task (nil = none); retained after execute for T1 priority
	taskRegistry map[string]map[string]*taskState // agentID → taskID → state (paused flag)
	sessionDone  map[string]chan struct{}      // agentID → buffered channel closed by OnStop when session fully ends
	confirmed    map[string]map[string]bool    // agentID → messageID → confirmed by a delivery tool callback this session
}

func newEngineState() *EngineState {
	return &EngineState{
		agents:       make(map[string]*AgentState),
		pending:      make(map[string]bool),
		sessions:     make(map[string]string),
		taskMux:      make(map[string]*TaskKey),
		taskRegistry: make(map[string]map[string]*taskState),
		sessionDone:  make(map[string]chan struct{}),
		confirmed:    make(map[string]map[string]bool),
	}
}

// BeginRunIfIdle atomically claims the agent for a delivery run.
// Returns true only if the agent exists, is not already Running, and is not interrupted —
// setting Status to Running under the same lock. This collapses the check-then-set in
// executeLoop into one atomic operation, closing the TOCTOU window where two concurrent
// executeLoop goroutines could both pass a plain Status==Running check and double-dispatch.
func (s *EngineState) BeginRunIfIdle(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.agents[agentID]
	if !ok || st.Status == AgentStatusRunning || st.CodeSession.IsInterrupted {
		return false
	}
	st.Status = AgentStatusRunning
	return true
}

// ConfirmMessage records that messageID was acknowledged by a delivery-confirming tool
// callback during agentID's current session (per-message, not session-wide).
func (s *EngineState) ConfirmMessage(agentID, messageID string) {
	if messageID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.confirmed[agentID] == nil {
		s.confirmed[agentID] = make(map[string]bool)
	}
	s.confirmed[agentID][messageID] = true
}

// IsMessageConfirmed reports whether messageID was confirmed by a tool callback this session.
func (s *EngineState) IsMessageConfirmed(agentID, messageID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m := s.confirmed[agentID]; m != nil {
		return m[messageID]
	}
	return false
}

// ClearConfirmed drops all per-message confirmations for agentID (call at session end).
func (s *EngineState) ClearConfirmed(agentID string) {
	s.mu.Lock()
	delete(s.confirmed, agentID)
	s.mu.Unlock()
}

// OpenSessionDone creates a buffered done channel for agentID's current session.
// The channel is signalled by SignalSessionDone when OnStop fully handles the session end.
func (s *EngineState) OpenSessionDone(agentID string) chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.sessionDone[agentID] = ch
	s.mu.Unlock()
	return ch
}

// SignalSessionDone closes the done channel for agentID, unblocking any waiter in executeLoop.
// Safe to call multiple times — extra signals are dropped by the buffered channel.
func (s *EngineState) SignalSessionDone(agentID string) {
	s.mu.RLock()
	ch := s.sessionDone[agentID]
	s.mu.RUnlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ClearSessionDone removes and returns the done channel for agentID.
func (s *EngineState) ClearSessionDone(agentID string) {
	s.mu.Lock()
	delete(s.sessionDone, agentID)
	s.mu.Unlock()
}

// SetSession records a sessionID → agentID mapping.
func (s *EngineState) SetSession(sessionID, agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = agentID
}

// ResolveSession looks up the agentID for a sessionID.
func (s *EngineState) ResolveSession(sessionID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.sessions[sessionID]
	return id, ok
}

// ClearSession removes a sessionID → agentID mapping.
func (s *EngineState) ClearSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// SetAgent stores a copy of state — callers own their local copy after this returns.
func (s *EngineState) SetAgent(agentID string, state AgentState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := state
	s.agents[agentID] = &cp
}

// GetAgent returns a copy of the stored state — safe to mutate without holding a lock.
func (s *EngineState) GetAgent(agentID string) (AgentState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.agents[agentID]; ok {
		return *st, true
	}
	return AgentState{}, false
}

// AgentIDs returns the IDs of all known agents.
func (s *EngineState) AgentIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.agents))
	for id := range s.agents {
		ids = append(ids, id)
	}
	return ids
}

// PendingAgentIDs returns the IDs of all agents currently marked pending.
func (s *EngineState) PendingAgentIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.pending))
	for id, p := range s.pending {
		if p {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *EngineState) SetPending(agentID string, pending bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[agentID] = pending
}

func (s *EngineState) IsPending(agentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pending[agentID]
}

// SetTaskMux records the active TaskKey for agentID (T1/T2).
// Pass nil to clear; retained after execute completes for T1 priority picking.
func (s *EngineState) SetTaskMux(agentID string, key *TaskKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.taskMux[agentID] = key
}

// GetTaskMux returns the active TaskKey for agentID, or nil if none.
func (s *EngineState) GetTaskMux(agentID string) *TaskKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.taskMux[agentID]
}

// PauseTask marks the task identified by (agentID, taskID) as paused (T4).
func (s *EngineState) PauseTask(agentID, taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskRegistry[agentID] == nil {
		s.taskRegistry[agentID] = make(map[string]*taskState)
	}
	s.taskRegistry[agentID][taskID] = &taskState{Paused: true}
}

// ResumeTask clears the paused flag for the task (T4).
func (s *EngineState) ResumeTask(agentID, taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.taskRegistry[agentID]; m != nil {
		delete(m, taskID)
	}
}

// IsTaskPaused reports whether the task is currently paused.
func (s *EngineState) IsTaskPaused(agentID, taskID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m := s.taskRegistry[agentID]; m != nil {
		if ts := m[taskID]; ts != nil {
			return ts.Paused
		}
	}
	return false
}
