package tui

import "github.com/charmbracelet/lipgloss"

var (
	styleBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1).
			Width(54)

	styleBoxError = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("196")).
			Padding(0, 1).
			Width(54)

	styleLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	styleValue = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Bold(true)

	styleOK = lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")).
		Bold(true)

	styleErr = lipgloss.NewStyle().
		Foreground(lipgloss.Color("196")).
		Bold(true)

	stylePhase = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))
)
