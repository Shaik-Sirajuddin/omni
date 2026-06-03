package tui

import (
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"

	operator "github.com/Shaik-Sirajuddin/memory/operator"
)

// Reporter implements operator.StatusReporter and forwards events to a
// bubbletea Program. Safe to call from any goroutine.
type Reporter struct {
	p    *tea.Program
	done chan struct{}
}

// NewReporter starts the bubbletea program writing to w and returns a
// Reporter. The program runs in the background; it quits automatically on
// Ready or Error. Call Wait() to block until the final frame is rendered.
func NewReporter(w io.Writer) *Reporter {
	p := tea.NewProgram(NewModel(), tea.WithOutput(w), tea.WithInput(nil))
	r := &Reporter{p: p, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		p.Run() //nolint:errcheck
	}()
	return r
}

// Wait blocks until the bubbletea program has exited (after Ready or Error).
func (r *Reporter) Wait() {
	if r == nil {
		return
	}
	<-r.done
}

// PhaseUpdate implements operator.StatusReporter.
func (r *Reporter) PhaseUpdate(op operator.InitPhase, detail string) {
	if r == nil {
		return
	}
	r.p.Send(MsgPhase{Phase: opPhase(op), Detail: detail})
}

// Ready implements operator.StatusReporter.
func (r *Reporter) Ready(agentName, model, sessionID string) {
	if r == nil {
		return
	}
	r.p.Send(MsgReady{AgentName: agentName, Model: model, SessionID: sessionID})
}

// Error implements operator.StatusReporter.
func (r *Reporter) Error(err error) {
	if r == nil {
		return
	}
	r.p.Send(MsgError{Err: err})
}

// IsTTY returns true when os.Stdout is an interactive terminal.
func IsTTY() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

func opPhase(op operator.InitPhase) Phase {
	switch op {
	case operator.InitPhaseStarting:
		return PhaseStarting
	case operator.InitPhaseWaiting:
		return PhaseWaiting
	default:
		return PhaseInit
	}
}
