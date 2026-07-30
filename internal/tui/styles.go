package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorGreen  = lipgloss.Color("#22c55e")
	colorYellow = lipgloss.Color("#eab308")
	colorRed    = lipgloss.Color("#ef4444")
	colorBlue   = lipgloss.Color("#3b82f6")
	colorGray   = lipgloss.Color("#6b7280")
	colorWhite  = lipgloss.Color("#f9fafb")
	colorBg     = lipgloss.Color("#111827")
	colorBorder = lipgloss.Color("#374151")

	styleTab = lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(lipgloss.Color("#d1d5db"))

	styleActiveTab = lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(colorWhite).
			Background(colorBlue).
			Bold(true)

	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBlue)

	styleOK = lipgloss.NewStyle().
		Foreground(colorGreen)

	styleWarn = lipgloss.NewStyle().
			Foreground(colorYellow)

	styleErr = lipgloss.NewStyle().
			Foreground(colorRed)

	styleDim = lipgloss.NewStyle().
			Foreground(colorGray)

	styleFocused = lipgloss.NewStyle().
			Foreground(colorWhite).
			Background(lipgloss.Color("#1e3a5f"))

	stylePanelBorder = lipgloss.NewStyle().
				Foreground(colorBorder)

	stylePanelTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBlue)

	styleStatusBar = lipgloss.NewStyle().
			Foreground(colorGray).
			Padding(0, 1)

	styleProgressBar = lipgloss.NewStyle().
				Foreground(colorBlue)

	styleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite).
			Padding(0, 1)

	styleDetailLabel = styleDim
	styleDetailValue = lipgloss.NewStyle().Foreground(colorWhite)

	// Settings screen: the value being typed, and its block cursor.
	styleEditText = lipgloss.NewStyle().Foreground(colorWhite)

	styleEditCursor = lipgloss.NewStyle().
			Foreground(colorBg).
			Background(colorYellow)

	styleFieldValue = lipgloss.NewStyle().Foreground(colorWhite)

	styleFieldLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("#d1d5db"))

	// Focused label: bold, but no padding — the settings rows align on a fixed
	// label column, so any padding would shift the row out of line.
	styleFieldLabelFocus = lipgloss.NewStyle().Bold(true).Foreground(colorWhite)
)
