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
			Foreground(colorGray).
			Background(lipgloss.Color("#1f2937"))

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

	styleBorder = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(colorBorder)

	styleStatusBar = lipgloss.NewStyle().
			Foreground(colorGray).
			Padding(0, 1)

	styleProgressBar = lipgloss.NewStyle().
				Foreground(colorBlue)

	styleDeviceOK  = styleOK
	styleDeviceOff = styleDim

	styleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite).
			Padding(0, 1)

	styleDetailLabel = styleDim
	styleDetailValue = lipgloss.NewStyle().Foreground(colorWhite)
)
