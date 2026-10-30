package style

import "github.com/charmbracelet/lipgloss"

var (
	Title = lipgloss.NewStyle().Bold(true)
	Note  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	Help  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	Error = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)
