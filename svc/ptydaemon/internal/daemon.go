package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	pkgpty "github.com/Shaik-Sirajuddin/memory/pkg/pty"
)

var ErrNotFound = errors.New("terminal not found")

// PTYDaemon manages the lifecycle of PTY sessions backed by a persistent store.
type PTYDaemon interface {
	Create(PTYCreateParams) (*PTYTerminalInfo, error)
	Adopt(agentID, sessionID string, pid int, submitKey string) error
	Pipe(agentID, sessionID string, data []byte) error
	Exec(agentID, sessionID, prompt string) error
	Stop(agentID, sessionID string) error
	List() ([]*PTYTerminalInfo, error)
	ListSessions(agentID string) ([]*PTYSessionRecord, error)
	GetSession(agentID, sessionID string) (*PTYSessionRecord, error)
	// GetMasterFd returns the PTY master file for the session.
	// The caller must not close the file; it is owned by the daemon.
	GetMasterFd(agentID, sessionID string) (*os.File, error)
	// StdinRelay reads from r and forwards each chunk to the PTY master while
	// tracking user input for ExecInSession write serialisation.
	// Blocks until r returns EOF/error or ctx is cancelled.
	StdinRelay(ctx context.Context, agentID, sessionID string, r io.Reader) error
	Shutdown(ctx context.Context) error
}

type defaultDaemon struct {
	store     *Store
	mu        sync.RWMutex
	terminals map[string]*PTYTerminal
}

func NewDaemon(store *Store) PTYDaemon {
	_ = store.MarkAllActiveCrashed()
	return &defaultDaemon{
		store:     store,
		terminals: make(map[string]*PTYTerminal),
	}
}

func termKey(agentID, sessionID string) string {
	return agentID + "/" + sessionID
}

func (d *defaultDaemon) get(agentID, sessionID string) *PTYTerminal {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// A session can have MORE THAN ONE terminal entry: the master-owning entry
	// from the Create/Start path (registered with an empty agentID, hasDrain=true)
	// and an adopted duplicate from the Register/Adopt path (registered with the
	// real agentID, holding its own non-owning fd). Writers MUST hit the master
	// owner — otherwise e.g. `pipe` (which passes the real agentID and would key
	// straight to the adopted duplicate) writes to an fd the child never reads,
	// so the bytes land nowhere. Resolve by sessionID and prefer the owner.
	//
	// Assumption: session IDs are globally unique (UUIDs from the operator); if two
	// agents shared one, an explicit agentID disambiguates below.
	var owner, fallback *PTYTerminal
	for _, t := range d.terminals {
		if t.SessionID != sessionID {
			continue
		}
		if agentID != "" && t.AgentID != "" && t.AgentID != agentID {
			continue // a genuinely different agent colliding on sessionID
		}
		fallback = t
		if t.hasDrain { // the Create/Start path owns and drains the master
			owner = t
		}
	}
	if owner != nil {
		return owner
	}
	return fallback
}

func (d *defaultDaemon) Create(p PTYCreateParams) (*PTYTerminalInfo, error) {
	key := termKey(p.AgentID, p.SessionID)

	d.mu.Lock()
	if _, exists := d.terminals[key]; exists {
		d.mu.Unlock()
		return nil, fmt.Errorf("terminal %s already exists", key)
	}
	d.mu.Unlock()

	cmd := exec.Command(p.Command, p.Args...)
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Dir = p.Dir

	ptm, err := pkgpty.StartCmd(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}
	master := ptm.Master()

	info := PTYTerminalInfo{
		AgentID:   p.AgentID,
		SessionID: p.SessionID,
		PID:       cmd.Process.Pid,
		Status:    StatusActive,
	}

	t := &PTYTerminal{
		PTYTerminalInfo: info,
		master:          master,
		cmd:             cmd,
		submitKey:       p.SubmitKey,
	}

	d.mu.Lock()
	d.terminals[key] = t
	d.mu.Unlock()

	if err := d.store.Insert(&info, p.SubmitKey); err != nil {
		return nil, fmt.Errorf("store insert: %w", err)
	}

	go watchTerminal(t, d.store, d.removeTerminal)
	t.startDrain() // drain output while detached so the child never blocks on write

	return &info, nil
}

// OnAttach is called when a client takes over the master fd for a session. It
// pauses the idle drainer so the client is the sole reader of the master. The
// redraw of the current screen is driven client-side (forceRedraw in the unix
// client), which can guarantee a genuine SIGWINCH against the real terminal size
// even when the PTY size is unchanged.
func (d *defaultDaemon) OnAttach(agentID, sessionID string) {
	if t := d.get(agentID, sessionID); t != nil {
		t.pauseDrain()
	}
}

// OnDetach is called when a client releases the master fd. It resumes the idle
// drainer so the kernel buffer keeps draining and the child does not stall.
func (d *defaultDaemon) OnDetach(agentID, sessionID string) {
	if t := d.get(agentID, sessionID); t != nil {
		t.resumeDrain()
	}
}

func (d *defaultDaemon) Pipe(agentID, sessionID string, data []byte) error {
	t := d.get(agentID, sessionID)
	if t == nil {
		return ErrNotFound
	}
	return t.writePipe(data)
}

func (d *defaultDaemon) Exec(agentID, sessionID, prompt string) error {
	t := d.get(agentID, sessionID)
	if t == nil {
		return ErrNotFound
	}
	return t.execPrompt(prompt)
}

func (d *defaultDaemon) Adopt(agentID, sessionID string, pid int, submitKey string) error {
	key := termKey(agentID, sessionID)

	d.mu.RLock()
	_, exists := d.terminals[key]
	d.mu.RUnlock()
	if exists {
		return nil // idempotent
	}

	// Recover submit_key from store when caller omits it on re-adopt after restart.
	// Non-fatal on error: exec falls back to plain \r.
	if submitKey == "" {
		rec, recErr := d.store.GetBySession(agentID, sessionID)
		if recErr != nil {
			_ = recErr // internal pkg has no logger; surfaced to caller if needed
		} else if rec != nil {
			submitKey = rec.SubmitKey
		}
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	master, err := os.OpenFile(fmt.Sprintf("/proc/%d/fd/0", pid), os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open pty fd for pid %d: %w", pid, err)
	}

	info := PTYTerminalInfo{
		AgentID:   agentID,
		SessionID: sessionID,
		PID:       pid,
		Status:    StatusActive,
	}

	t := &PTYTerminal{
		PTYTerminalInfo: info,
		master:          master,
		cmd:             nil,
		proc:            proc,
		submitKey:       submitKey,
	}

	d.mu.Lock()
	d.terminals[key] = t
	d.mu.Unlock()

	if err := d.store.Insert(&info, submitKey); err != nil {
		return fmt.Errorf("store insert: %w", err)
	}

	go watchAdopted(t, d.store, d.removeTerminal)

	return nil
}

func (d *defaultDaemon) Stop(agentID, sessionID string) error {
	t := d.get(agentID, sessionID)
	if t == nil {
		return ErrNotFound
	}
	if t.cmd == nil {
		// Adopted session: use t.AgentID/t.SessionID (not the caller's agentID param,
		// which may be "" when the caller scanned by sessionID only). Using the params
		// targets the wrong DB row and the wrong in-memory key when agentID == "".
		t.setStatus(StatusStopped)
		_ = d.store.UpdateStatus(t.AgentID, t.SessionID, StatusStopped)
		t.closeMaster()
		d.removeTerminal(t.AgentID, t.SessionID)
		if t.proc != nil {
			_ = t.proc.Kill()
		}
		// Also remove any Start-path (agentID="") terminal for the same sessionID so
		// Create does not falsely report "already exists" on the next Resume.
		d.removeAllBySessionID(sessionID)
		return nil
	}
	// Remove from the in-memory map immediately so a concurrent Create for the
	// same session does not race against watchTerminal's async removal.
	// watchTerminal will call removeTerminal again after cmd.Wait() — that is a
	// no-op since the key is already gone.
	d.removeTerminal(t.AgentID, t.SessionID)
	// Also synchronously mark stopped in the store so that List (which queries the DB)
	// reflects the correct state before watchTerminal's async UpdateStatus fires.
	_ = d.store.UpdateStatus(t.AgentID, t.SessionID, StatusStopped)
	return t.kill()
}

// removeAllBySessionID removes every terminal whose SessionID matches,
// regardless of AgentID. Used after stopping one terminal to sweep up any
// duplicate entries for the same session (e.g. a Start-path "" AgentID entry
// that coexists with an Adopt-path entry).
func (d *defaultDaemon) removeAllBySessionID(sessionID string) {
	d.mu.Lock()
	var keys []string
	for k, t := range d.terminals {
		if t.SessionID == sessionID {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		delete(d.terminals, k)
	}
	d.mu.Unlock()
}

func (d *defaultDaemon) List() ([]*PTYTerminalInfo, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	infos := make([]*PTYTerminalInfo, 0, len(d.terminals))
	for _, t := range d.terminals {
		t.mu.Lock()
		cp := t.PTYTerminalInfo
		t.mu.Unlock()
		infos = append(infos, &cp)
	}
	return infos, nil
}

func (d *defaultDaemon) Shutdown(ctx context.Context) error {
	d.mu.RLock()
	terminals := make([]*PTYTerminal, 0, len(d.terminals))
	for _, t := range d.terminals {
		terminals = append(terminals, t)
	}
	d.mu.RUnlock()

	for _, t := range terminals {
		_ = t.kill()
	}
	return nil
}

func (d *defaultDaemon) ListSessions(agentID string) ([]*PTYSessionRecord, error) {
	return d.store.ListByAgent(agentID)
}

func (d *defaultDaemon) GetSession(agentID, sessionID string) (*PTYSessionRecord, error) {
	return d.store.GetBySession(agentID, sessionID)
}

// GetMasterFd returns the PTY master file for the given session.
// The returned file is owned by the daemon; callers must not close it.
func (d *defaultDaemon) GetMasterFd(agentID, sessionID string) (*os.File, error) {
	t := d.get(agentID, sessionID)
	if t == nil {
		return nil, ErrNotFound
	}
	t.mu.Lock()
	f := t.master
	t.mu.Unlock()
	if f == nil {
		return nil, errors.New("terminal has no master fd")
	}
	return f, nil
}

func (d *defaultDaemon) removeTerminal(agentID, sessionID string) {
	d.mu.Lock()
	delete(d.terminals, termKey(agentID, sessionID))
	d.mu.Unlock()
}

// chunkPool recycles the short-lived stdin chunks copied out of the relay read
// buffer. Each chunk lives only for one StdinRelay loop iteration.
var chunkPool = sync.Pool{New: func() any {
	b := make([]byte, 0, 4096)
	return &b
}}

// getChunk returns a slice of length n backed by pooled storage when possible.
func getChunk(n int) []byte {
	ptr := chunkPool.Get().(*[]byte)
	b := *ptr
	if cap(b) < n {
		return make([]byte, n)
	}
	return b[:n]
}

// putChunk returns a chunk to the pool. The caller must not reference it after.
func putChunk(b []byte) {
	s := b[:0]
	chunkPool.Put(&s)
}

// StdinRelay reads from r in a loop, writing each chunk to the PTY first,
// then tracking it in the input queue — so the queue only contains what was
// successfully written.
func (d *defaultDaemon) StdinRelay(ctx context.Context, agentID, sessionID string, r io.Reader) error {
	t := d.get(agentID, sessionID)
	if t == nil {
		return ErrNotFound
	}
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		n, err := r.Read(buf)
		if n > 0 {
			// chunk is consumed synchronously by writeUser + trackUserInput (both
			// copy out the bytes and retain no reference), so it can be pooled
			// and returned at the end of the iteration instead of heap-allocated
			// on every read.
			chunk := getChunk(n)
			copy(chunk, buf[:n])
			// writeUser serialises against execPrompt so a keystroke can never
			// land inside an in-flight exec sequence; it is deferred until exec
			// completes, then surfaces after the prompt.
			werr := t.writeUser(chunk)
			if werr == nil {
				t.trackUserInput(chunk)
			}
			putChunk(chunk)
			if werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
