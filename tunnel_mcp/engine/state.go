package engine

import "sync"

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
	IsInterrupted        bool
	SessionID            string
	MandatoryToolInvoked bool // set true when a delivery-confirming tool fires: send_response / send_response_batch (canonical) or legacy aliases query_result, update_message, etc.
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
	mu       sync.RWMutex
	agents   map[string]*AgentState
	pending  map[string]bool   // agentID → has undelivered messages
	sessions map[string]string // sessionID → agentID
}

func newEngineState() *EngineState {
	return &EngineState{
		agents:   make(map[string]*AgentState),
		pending:  make(map[string]bool),
		sessions: make(map[string]string),
	}
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
