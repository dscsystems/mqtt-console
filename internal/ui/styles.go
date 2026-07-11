package ui

import "github.com/charmbracelet/lipgloss"

var (
	colAccent  = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}   // blue
	colDim     = lipgloss.AdaptiveColor{Light: "245", Dark: "241"} // grey
	colGood    = lipgloss.AdaptiveColor{Light: "28", Dark: "40"}   // green
	colWarn    = lipgloss.AdaptiveColor{Light: "130", Dark: "214"} // orange
	colBad     = lipgloss.AdaptiveColor{Light: "124", Dark: "196"} // red
	colBadgeFg = lipgloss.AdaptiveColor{Light: "255", Dark: "232"}

	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stDim      = lipgloss.NewStyle().Foreground(colDim)
	stGood     = lipgloss.NewStyle().Foreground(colGood)
	stWarn     = lipgloss.NewStyle().Foreground(colWarn)
	stBad      = lipgloss.NewStyle().Foreground(colBad)
	stSelected = lipgloss.NewStyle().Reverse(true)
	stKey      = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	stRetained = lipgloss.NewStyle().Foreground(colBadgeFg).Background(colWarn).Padding(0, 1)

	stHeader = lipgloss.NewStyle().Bold(true)
	stBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colDim)
	stFocusBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent)
	stModal = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colAccent).
		Padding(1, 2)
)
