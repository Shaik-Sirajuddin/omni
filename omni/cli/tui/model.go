package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// Model is the bubbletea model for agent init status.
type Model struct {
	phase     Phase
	detail    string
	agentName string
	model     string
	sessionID string
	err       error
	spinner   spinner.Model
	done      bool
}

func NewModel() Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return Model{
		phase:   PhaseInit,
		detail:  "Resolving agent…",
		spinner: s,
	}
}

func (m Model) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case MsgPhase:
		m.phase = msg.Phase
		m.detail = msg.Detail
		return m, nil

	case MsgReady:
		m.phase = PhaseActive
		m.agentName = msg.AgentName
		m.model = msg.Model
		m.sessionID = msg.SessionID
		m.done = true
		return m, tea.Quit

	case MsgError:
		m.phase = PhaseError
		m.err = msg.Err
		m.done = true
		return m, tea.Quit

	case tea.KeyMsg:
		if m.done {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) View() string {
	if m.phase == PhaseError {
		body := strings.Join([]string{
			styleErr.Render("✗ Initialisation failed"),
			"",
			styleLabel.Render(errString(m.err)),
		}, "\n")
		return styleBoxError.Render(body) + "\n"
	}

	if m.phase == PhaseActive {
		rows := []string{
			styleOK.Render("✓ Session active"),
		}
		if m.agentName != "" {
			rows = append(rows, styleLabel.Render("Agent   ")+styleValue.Render(m.agentName))
		}
		if m.sessionID != "" {
			rows = append(rows, styleLabel.Render("Session ")+styleValue.Render(shortID(m.sessionID)))
		}
		if m.model != "" {
			rows = append(rows, styleLabel.Render("Model   ")+styleValue.Render(m.model))
		}
		return styleBox.Render(strings.Join(rows, "\n")) + "\n"
	}

	label := phaseLabel(m.phase)
	rows := []string{
		m.spinner.View() + " " + stylePhase.Render(label),
	}
	if m.detail != "" {
		rows = append(rows, "  "+styleLabel.Render(m.detail))
	}
	return styleBox.Render(strings.Join(rows, "\n")) + "\n"
}

func phaseLabel(p Phase) string {
	switch p {
	case PhaseInit:
		return "Initialising…"
	case PhaseStarting:
		return "Starting session…"
	case PhaseWaiting:
		return "Waiting for PTY…"
	default:
		return "Working…"
	}
}

func errString(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
