package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Eric-Eklund/camera-backup/internal/preview"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
)

// renderMain renders the three-panel main screen.
func (m *Model) renderMain() string {
	if m.width == 0 || m.height == 0 {
		return m.statusMsg
	}

	totalH := m.height

	// ── tab bar ──────────────────────────────────────────────────────────────
	tabBar := m.renderTabBar()
	tabH := 1

	// ── status bar at bottom ─────────────────────────────────────────────────
	hints := "[tab] tabs  [j/k] move  [enter] expand/preview  [space] select  [a] all  [g] grid  [y] copy  [v] verify  [q] quit"
	if n := len(m.selected); n > 0 {
		var selBytes int64
		for _, f := range m.allFiles {
			if m.selected[f.AbsPath] {
				selBytes += f.Size
			}
		}
		hints = fmt.Sprintf("%d selected · %s   %s", n, fmtBytes(selBytes), hints)
	}
	keyHints := styleStatusBar.Render(hints)
	statusH := 1

	// ── middle section ────────────────────────────────────────────────────────
	midH := totalH - tabH - statusH - 1 // -1 for newlines
	if midH < 4 {
		midH = 4
	}

	// Panel widths.
	devW := 22
	detW := 30
	if m.width < devW+detW+20 {
		devW = 0
		detW = 0
	}
	treeW := m.width - devW - detW

	devPanel := m.renderDevicePanel(devW, midH)
	treePanel := m.renderTreePanel(treeW, midH)
	detPanel := m.renderDetailPanel(detW, midH)

	mid := lipgloss.JoinHorizontal(lipgloss.Top, devPanel, treePanel, detPanel)

	// ── status bar ───────────────────────────────────────────────────────────
	scanMsg := ""
	if m.statusMsg != "" {
		scanMsg = styleWarn.Render("  "+m.statusMsg) + "  "
	}

	return tabBar + "\n" + mid + "\n" + scanMsg + keyHints
}

func (m *Model) renderTabBar() string {
	var parts []string
	for i, tab := range m.tabs {
		if i == m.activeTab {
			parts = append(parts, styleActiveTab.Render("[ "+tab+" ]"))
		} else {
			parts = append(parts, styleTab.Render(tab))
		}
	}
	return strings.Join(parts, "  ")
}

func (m *Model) renderDevicePanel(w, h int) string {
	if w == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(styleHeader.Render("Devices") + "\n")

	if m.status != nil {
		r := m.status
		sb.WriteString(renderDevice("Camera", r.SourceAvail, r.SourceFree) + "\n")
		sb.WriteString(renderDevice("SSD   ", r.SSDAvail, r.SSDFree) + "\n")
		sb.WriteString(renderDevice("NAS   ", r.NASAvail, r.NASFree) + "\n")
	}

	lines := strings.Split(sb.String(), "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	lines = lines[:h]

	// Pad each line to width.
	for i, l := range lines {
		lines[i] = lipgloss.NewStyle().Width(w).Render(l)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderTreePanel(w, h int) string {
	if w <= 0 {
		return ""
	}
	var lines []string

	for i, node := range m.visible {
		focused := i == m.cursor
		line := m.renderNode(node, w, focused)
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		empty := styleDim.Render("  (no files)")
		lines = append(lines, empty)
	}

	// Show a window of h lines around the cursor.
	start := 0
	if m.cursor >= h {
		start = m.cursor - h/2
	}
	if start+h > len(lines) {
		start = len(lines) - h
	}
	if start < 0 {
		start = 0
	}
	end := start + h
	if end > len(lines) {
		end = len(lines)
	}

	window := lines[start:end]
	for len(window) < h {
		window = append(window, lipgloss.NewStyle().Width(w).Render(""))
	}
	return strings.Join(window, "\n")
}

func (m *Model) renderNode(node treeNode, w int, focused bool) string {
	indent := strings.Repeat("  ", node.level)
	var label string

	switch node.level {
	case 0: // year
		icon := "▶"
		if node.expanded {
			icon = "▼"
		}
		label = fmt.Sprintf("%s %s %s", indent, icon, node.year)

	case 1: // month
		icon := "▶"
		if node.expanded {
			icon = "▼"
		}
		label = fmt.Sprintf("%s %s %s", indent, icon, node.month)

	case 2: // day (date group)
		icon := "▶"
		if node.expanded {
			icon = "▼"
		}
		files := m.dayFiles(node.year, node.month, node.day)
		var totalSize int64
		for _, f := range files {
			totalSize += f.Size
		}
		label = fmt.Sprintf("%s %s %s  (%d · %s)",
			indent, icon, node.day, len(files), fmtBytes(totalSize))

	case 3: // file
		files := m.dayFiles(node.year, node.month, node.day)
		if node.fileIdx >= len(files) {
			return ""
		}
		f := files[node.fileIdx]
		onSSD, onNAS := m.fileStatus(f)
		ssdIcon := styleErr.Render("✗SSD")
		if onSSD {
			ssdIcon = styleOK.Render("✓SSD")
		}
		nasIcon := ""
		if m.status != nil && m.status.NASAvail {
			if onNAS {
				nasIcon = " " + styleOK.Render("✓NAS")
			} else {
				nasIcon = " " + styleErr.Render("✗NAS")
			}
		}
		mark := "  "
		if m.selected[f.AbsPath] {
			mark = styleWarn.Render("● ")
		}
		name := filepath.Base(f.RelPath)
		label = fmt.Sprintf("%s%s%-22s %8s  %s%s",
			indent, mark, truncate(name, 22), fmtBytes(f.Size), ssdIcon, nasIcon)
	}

	s := lipgloss.NewStyle().Width(w)
	if focused {
		s = s.Background(lipgloss.Color("#1e3a5f")).Foreground(colorWhite)
	}
	return s.Render(label)
}

func (m *Model) fileStatus(f scan.FileInfo) (onSSD, onNAS bool) {
	if m.status == nil {
		return
	}
	key := f.DestKey(m.cfg.Category(f.RelPath))
	return m.ssdKeys[key], m.nasKeys[key]
}

func (m *Model) renderDetailPanel(w, h int) string {
	if w <= 0 {
		return ""
	}
	var lines []string

	f := m.currentFile()
	if f == nil {
		// Show date group summary if on a day node.
		if len(m.visible) > 0 && m.cursor < len(m.visible) {
			node := m.visible[m.cursor]
			if node.level == 2 {
				lines = m.renderDayDetail(node, w, h)
			}
		}
	} else {
		lines = m.renderFileDetail(f, w, h)
	}

	for len(lines) < h {
		lines = append(lines, lipgloss.NewStyle().Width(w).Render(""))
	}
	return strings.Join(lines[:h], "\n")
}

func (m *Model) renderFileDetail(f *scan.FileInfo, w, h int) []string {
	var lines []string
	add := func(s string) {
		lines = append(lines, lipgloss.NewStyle().Width(w).Render(s))
	}

	add(styleHeader.Render(filepath.Base(f.RelPath)))
	add(styleDetailLabel.Render("Size:  ") + styleDetailValue.Render(fmtBytes(f.Size)))
	add(styleDetailLabel.Render("Date:  ") + styleDetailValue.Render(f.ModTime.Format("2006-01-02")))
	add(styleDetailLabel.Render("Time:  ") + styleDetailValue.Render(f.ModTime.Format("15:04:05")))
	add(styleDetailLabel.Render("Src:   ") + styleDetailValue.Render(truncate(f.AbsPath, w-8)))
	add("")

	// Thumbnail.
	thumbH := h - len(lines) - 1
	thumbW := w - 2
	if thumbH > 4 && thumbW > 4 {
		if img, ok := m.thumbCache[f.AbsPath]; ok {
			if img != nil {
				art := preview.BlockArt(img, thumbW, thumbH)
				for _, al := range strings.Split(strings.TrimRight(art, "\n"), "\n") {
					lines = append(lines, al)
				}
			} else {
				add(styleDim.Render("  [no preview]"))
			}
		} else {
			add(styleDim.Render("  Loading…"))
		}
	}

	return lines
}

func (m *Model) renderDayDetail(node treeNode, w, h int) []string {
	var lines []string
	add := func(s string) {
		lines = append(lines, lipgloss.NewStyle().Width(w).Render(s))
	}

	files := m.dayFiles(node.year, node.month, node.day)
	var totalSize int64
	for _, f := range files {
		totalSize += f.Size
	}
	add(styleHeader.Render(node.day))
	add(fmt.Sprintf("  %d files · %s", len(files), fmtBytes(totalSize)))
	add("")

	// Mini-grid: first 6 thumbnails as block art.
	maxPrev := 6
	if len(files) < maxPrev {
		maxPrev = len(files)
	}
	thumbW := (w - 2) / 3
	if thumbW < 4 {
		thumbW = 4
	}
	thumbH := 4
	for i := 0; i < maxPrev; i++ {
		f := files[i]
		if img, ok := m.thumbCache[f.AbsPath]; ok && img != nil {
			art := preview.BlockArt(img, thumbW, thumbH)
			for _, al := range strings.Split(strings.TrimRight(art, "\n"), "\n") {
				lines = append(lines, al)
			}
		}
	}

	return lines
}

// renderGrid renders the full-screen thumbnail grid for a date group.
func (m *Model) renderGrid() string {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	cols := m.gridCols()
	thumbCellW := m.width / cols
	thumbH := 8

	var sb strings.Builder
	sb.WriteString(styleTitle.Render(fmt.Sprintf("  %s · %d files", m.gridDay, len(files))) + "\n\n")

	for i, f := range files {
		col := i % cols
		name := filepath.Base(f.RelPath)

		focused := i == m.gridCursor
		label := truncate(name, thumbCellW-2)
		if focused {
			label = styleWarn.Render("▶ " + label)
		} else {
			label = "  " + label
		}

		if col == 0 && i > 0 {
			sb.WriteString("\n")
		}

		// Render thumbnail or placeholder.
		var art string
		if img, ok := m.thumbCache[f.AbsPath]; ok && img != nil {
			art = preview.BlockArt(img, thumbCellW-2, thumbH)
		} else {
			// Placeholder box.
			art = strings.Repeat(styleDim.Render(strings.Repeat("░", thumbCellW-2))+"\n", thumbH)
		}

		sb.WriteString(art)
		sb.WriteString(label + "\n")
	}

	sb.WriteString("\n" + styleStatusBar.Render("[←→↑↓] navigate  [Enter/p] preview  [Esc] back"))
	return sb.String()
}

// renderPreview renders the full-screen preview screen.
// With Kitty graphics support the image area is left blank — the image itself
// is drawn on top by kittyDrawCmd after the frame is flushed.
func (m *Model) renderPreview() string {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	if m.gridCursor >= len(files) {
		return "No file selected."
	}
	f := files[m.gridCursor]
	name := filepath.Base(f.RelPath)

	previewH := m.height - 4
	previewW := m.width - 4
	if previewH < 1 {
		previewH = 1
	}
	if previewW < 1 {
		previewW = 1
	}

	// Best available image: full-size if loaded, else thumbnail.
	img, haveFull := m.fullCache[f.AbsPath]
	if !haveFull || img == nil {
		if t, ok := m.thumbCache[f.AbsPath]; ok {
			img = t
		}
	}

	var content string
	switch {
	case m.kitty && img != nil:
		// Reserve blank lines; the Kitty image is drawn over this area.
		content = strings.Repeat("\n", previewH)
	case img != nil:
		content = preview.BlockArt(img, previewW, previewH)
	case haveFull:
		content = styleDim.Render("\n\n  [no preview available]")
	default:
		content = styleDim.Render("\n\n  Loading…")
	}

	header := styleTitle.Render(name) + "  " +
		styleDim.Render(fmtBytes(f.Size)+" · "+f.ModTime.Format("2006-01-02 15:04"))

	hint := styleStatusBar.Render("[←/→] prev/next  [Esc] back")

	return header + "\n" + content + "\n" + hint
}

// renderProgress renders the copy/verify progress screen.
func (m *Model) renderProgress() string {
	var sb strings.Builder

	title := "Copying…"
	switch m.progressMode {
	case modePhase1:
		title = "Phase 1: Camera → SSD (with verification)"
	case modePhase2:
		title = "Phase 2: SSD → NAS"
	case modeSync:
		title = "Sync: SSD → NAS"
	case modeVerify:
		title = "Verifying files…"
	}
	sb.WriteString(styleTitle.Render(title) + "\n\n")

	if m.progressMode == modeVerify {
		return sb.String() + m.renderVerifyProgress()
	}

	// Per-file progress bars in stable first-seen order.
	barW := m.width - 52
	if barW < 10 {
		barW = 10
	}
	shown := 0
	maxShown := m.height - 8
	if maxShown < 1 {
		maxShown = 1
	}
	var bytesWritten int64
	for _, rel := range m.progressOrder {
		fp := m.fileProgress[rel]
		bytesWritten += fp.Written
		if fp.Done || shown >= maxShown {
			continue
		}
		pct := 0
		if fp.Size > 0 {
			pct = int(fp.Written * 100 / fp.Size)
		}
		speed := ""
		if start, ok := m.fileStart[rel]; ok {
			if elapsed := time.Since(start).Seconds(); elapsed > 0.2 {
				speed = fmtBytes(int64(float64(fp.Written)/elapsed)) + "/s"
			}
		}
		name := truncate(filepath.Base(fp.RelPath), 30)
		bar := progressBar(barW, int(fp.Written), int(fp.Size))
		sb.WriteString(fmt.Sprintf("  %-30s  %s  %3d%%  %10s\n", name, bar, pct, speed))
		shown++
	}

	// Overall progress by bytes, with file count.
	sb.WriteString("\n")
	overallBar := progressBar(m.width-30, int(bytesWritten), int(m.copyBytes))
	sb.WriteString(fmt.Sprintf("  Overall: %s  %d/%d files · %s / %s\n",
		overallBar, m.copyDone, m.copyTotal,
		fmtBytes(bytesWritten), fmtBytes(m.copyBytes)))

	if m.failures > 0 {
		sb.WriteString(styleErr.Render(fmt.Sprintf("\n  %d file(s) failed so far.", m.failures)) + "\n")
	}

	return sb.String()
}

// renderVerifyProgress renders the live verify view: overall bar + issue list.
func (m *Model) renderVerifyProgress() string {
	var sb strings.Builder

	if m.verifyTotal == 0 {
		sb.WriteString(styleDim.Render("  Scanning files…") + "\n")
		return sb.String()
	}

	bar := progressBar(m.width-30, m.verifyDone, m.verifyTotal)
	sb.WriteString(fmt.Sprintf("  %s  %d / %d files\n\n", bar, m.verifyDone, m.verifyTotal))

	if len(m.verifyIssues) == 0 {
		sb.WriteString(styleOK.Render("  No issues found so far.") + "\n")
		return sb.String()
	}

	maxShown := m.height - 10
	if maxShown < 1 {
		maxShown = 1
	}
	start := 0
	if len(m.verifyIssues) > maxShown {
		start = len(m.verifyIssues) - maxShown
	}
	for _, r := range m.verifyIssues[start:] {
		sb.WriteString(styleWarn.Render(fmt.Sprintf("  ⚠ %s — %s",
			filepath.Base(r.RelPath), strings.Join(r.Issues, ", "))) + "\n")
	}
	return sb.String()
}

// renderDone renders the completion screen.
func (m *Model) renderDone() string {
	var sb strings.Builder
	sb.WriteString(styleOK.Render("  Done!") + "\n\n")
	sb.WriteString("  " + m.doneMsg + "\n")

	if m.failures > 0 {
		sb.WriteString(styleErr.Render(fmt.Sprintf("\n  %d file(s) had errors.", m.failures)) + "\n")
		sb.WriteString("  " + styleWarn.Render("[e]") + " View error details\n")
	}
	if len(m.verifyIssues) > 0 {
		sb.WriteString("  " + styleWarn.Render("[e]") + " View verify issues\n")
	}

	sb.WriteString("\n  " + styleOK.Render("[r]") + " Rescan and return to main screen\n")
	sb.WriteString("  " + styleDim.Render("[q]") + " Quit\n")
	return sb.String()
}

// renderErrors renders the error summary screen (copy failures and verify issues).
func (m *Model) renderErrors() string {
	var sb strings.Builder
	sb.WriteString(styleTitle.Render("  Error Summary") + "\n\n")

	if len(m.failedFiles) == 0 && len(m.verifyIssues) == 0 {
		sb.WriteString(styleDim.Render("  No errors recorded.\n"))
	}
	for _, fp := range m.failedFiles {
		name := truncate(filepath.Base(fp.RelPath), 40)
		errMsg := ""
		if fp.Err != nil {
			errMsg = fp.Err.Error()
		}
		sb.WriteString(styleErr.Render(fmt.Sprintf("  %-40s  %s\n", name, errMsg)))
	}
	for _, r := range m.verifyIssues {
		name := truncate(filepath.Base(r.RelPath), 40)
		sb.WriteString(styleWarn.Render(fmt.Sprintf("  %-40s  %s\n", name, strings.Join(r.Issues, ", "))))
	}

	sb.WriteString("\n  " + styleDim.Render("[Esc/q]") + " Back\n")
	return sb.String()
}
