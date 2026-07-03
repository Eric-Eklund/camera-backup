package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Eric-Eklund/camera-backup/internal/preview"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
)

// panel renders content inside a rounded border with the title embedded in
// the top edge, Yazi-style: ╭─ Title ─────╮. w and h are outer dimensions;
// content is clipped and padded to the inner (w-2)×(h-2) area.
func panel(title, content string, w, h int) string {
	innerW := w - 2
	innerH := h - 2
	if innerW < 1 || innerH < 0 {
		return ""
	}

	t := ""
	if title != "" && lipgloss.Width(title)+4 <= innerW {
		t = " " + title + " "
	}
	fill := innerW - 1 - lipgloss.Width(t)
	top := stylePanelBorder.Render("╭─") +
		stylePanelTitle.Render(t) +
		stylePanelBorder.Render(strings.Repeat("─", fill)+"╮")

	side := stylePanelBorder.Render("│")
	lines := strings.Split(content, "\n")

	var sb strings.Builder
	sb.WriteString(top + "\n")
	for i := 0; i < innerH; i++ {
		line := ""
		if i < len(lines) {
			line = ansi.Truncate(lines[i], innerW, "")
		}
		if pad := innerW - lipgloss.Width(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		sb.WriteString(side + line + side + "\n")
	}
	sb.WriteString(stylePanelBorder.Render("╰" + strings.Repeat("─", innerW) + "╯"))
	return sb.String()
}

// screenFrame wraps a full-screen body in a titled border with a hint line
// below the frame, so every screen shares the same look.
func (m *Model) screenFrame(title, body, hint string) string {
	if m.width == 0 || m.height == 0 {
		return body
	}
	frame := panel(title, body, m.width, m.height-1)
	return frame + "\n" + lipgloss.NewStyle().MaxWidth(m.width).Render(styleStatusBar.Render(hint))
}

// renderMain renders the main screen: header row (tabs + devices), the Files
// and Info panels, and a status bar.
func (m *Model) renderMain() string {
	if m.width == 0 || m.height == 0 {
		return m.statusMsg
	}

	midH := m.height - 2 // header + status bar
	if midH < 4 {
		midH = 4
	}

	detW := 34
	if m.width < 70 {
		detW = 0
	}
	treeW := m.width - detW

	mid := panel("Files", m.renderTreePanel(treeW-2, midH-2), treeW, midH)
	if detW > 0 {
		detPanel := panel("Info", m.renderDetailPanel(detW-2, midH-2), detW, midH)
		mid = lipgloss.JoinHorizontal(lipgloss.Top, mid, detPanel)
	}

	return m.renderHeaderRow() + "\n" + mid + "\n" + m.renderStatusBar()
}

// renderHeaderRow lays out the tab bar on the left and device status on the right.
func (m *Model) renderHeaderRow() string {
	tabs := m.renderTabBar()
	dev := m.renderDeviceHeader()
	gap := m.width - lipgloss.Width(tabs) - lipgloss.Width(dev)
	if gap < 1 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(tabs)
	}
	return tabs + strings.Repeat(" ", gap) + dev
}

// renderDeviceHeader renders the compact device status: ✔ Camera · ✔ SSD 412 GB · ✘ NAS
func (m *Model) renderDeviceHeader() string {
	if m.status == nil {
		return ""
	}
	r := m.status
	sep := styleDim.Render(" · ")
	return deviceBadge("Camera", r.SourceAvail, r.SourceFree) + sep +
		deviceBadge("SSD", r.SSDAvail, r.SSDFree) + sep +
		deviceBadge("NAS", r.NASAvail, r.NASFree) + " "
}

func deviceBadge(name string, avail bool, free int64) string {
	if !avail {
		return styleDim.Render("✘ " + name)
	}
	s := styleOK.Render("✔ " + name)
	if free > 0 {
		s += styleDim.Render(" " + fmtBytes(free))
	}
	return s
}

func (m *Model) renderStatusBar() string {
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
	bar := styleStatusBar.Render(hints)
	if m.statusMsg != "" {
		bar = styleWarn.Render(" "+m.statusMsg) + "  " + bar
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(bar)
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
		// Shrink the name column on narrow panels so the size and the
		// SSD/NAS status icons always stay visible.
		nameW := w - 28
		if nameW > 22 {
			nameW = 22
		}
		if nameW < 8 {
			nameW = 8
		}
		name := filepath.Base(f.RelPath)
		label = fmt.Sprintf("%s%s%-*s %8s  %s%s",
			indent, mark, nameW, truncate(name, nameW), fmtBytes(f.Size), ssdIcon, nasIcon)
	}

	// Truncate before applying Width — lipgloss wraps overlong content, which
	// would break the one-line-per-node invariant of the tree.
	label = ansi.Truncate(label, w, "…")
	s := lipgloss.NewStyle().Width(w)
	if focused {
		s = styleFocused.Width(w)
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
		lines = append(lines, lipgloss.NewStyle().Width(w).Render(ansi.Truncate(s, w, "…")))
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
		lines = append(lines, lipgloss.NewStyle().Width(w).Render(ansi.Truncate(s, w, "…")))
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

// renderGrid renders the full-screen thumbnail grid for a date group,
// scrolled so the cursor row is visible.
func (m *Model) renderGrid() string {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	cols := m.gridCols()
	thumbCellW := (m.width - 2) / cols
	totalRows := (len(files) + cols - 1) / cols
	visRows := m.gridVisibleRows()

	// Clamp the scroll offset (window resizes can invalidate it).
	if m.gridOffset > totalRows-visRows {
		m.gridOffset = totalRows - visRows
	}
	if m.gridOffset < 0 {
		m.gridOffset = 0
	}

	// Build one cell (thumbnail + label) per visible file, join into rows.
	start := m.gridOffset * cols
	end := (m.gridOffset + visRows) * cols
	if end > len(files) {
		end = len(files)
	}

	var sb strings.Builder
	for rowStart := start; rowStart < end; rowStart += cols {
		rowEnd := rowStart + cols
		if rowEnd > end {
			rowEnd = end
		}
		cells := make([]string, 0, cols)
		for i := rowStart; i < rowEnd; i++ {
			f := files[i]
			name := filepath.Base(f.RelPath)

			prefix := "  "
			switch {
			case i == m.gridCursor && m.selected[f.AbsPath]:
				prefix = styleWarn.Render("▶●")
			case i == m.gridCursor:
				prefix = styleWarn.Render("▶ ")
			case m.selected[f.AbsPath]:
				prefix = styleWarn.Render("● ")
			}
			label := prefix + truncate(name, thumbCellW-3)

			var art string
			if img, ok := m.thumbCache[f.AbsPath]; ok && img != nil {
				art = preview.BlockArt(img, thumbCellW-2, gridThumbH)
			} else {
				// Placeholder box.
				art = strings.TrimRight(
					strings.Repeat(styleDim.Render(strings.Repeat("░", thumbCellW-2))+"\n", gridThumbH), "\n")
			}

			cells = append(cells, lipgloss.NewStyle().Width(thumbCellW).Render(
				strings.TrimRight(art, "\n")+"\n"+label))
		}
		sb.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, cells...))
		sb.WriteString("\n")
	}

	title := fmt.Sprintf("%s · %d files", m.gridDay, len(files))
	if totalRows > visRows {
		title += fmt.Sprintf(" · rows %d–%d/%d", m.gridOffset+1, m.gridOffset+visRows, totalRows)
	}
	hint := fmt.Sprintf("file %d/%d   [←→↑↓] navigate  [Enter/p] preview  [space] select  [y] copy  [Esc] back",
		m.gridCursor+1, len(files))
	if m.statusMsg != "" {
		hint = styleWarn.Render(m.statusMsg) + "   " + hint
	}
	return m.screenFrame(title, sb.String(), hint)
}

// renderPreview renders the full-screen preview screen.
// With Kitty graphics support the image area is left blank — the image itself
// is drawn on top by kittyDrawCmd after the frame is flushed (at cell (3,3),
// which lands inside this screen's border).
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

	meta := styleDim.Render(fmt.Sprintf(" %s · %s · %d/%d",
		fmtBytes(f.Size), f.ModTime.Format("2006-01-02 15:04"), m.gridCursor+1, len(files)))

	return m.screenFrame(name, meta+"\n"+content, "[←/→] prev/next  [Esc] back")
}

// renderProgress renders the copy/verify progress screen.
func (m *Model) renderProgress() string {
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

	if m.progressMode == modeVerify {
		return m.screenFrame(title, m.renderVerifyProgress(), "[q] quit")
	}

	var sb strings.Builder
	sb.WriteString("\n")

	// Per-file progress bars in stable first-seen order.
	barW := m.width - 54
	if barW < 10 {
		barW = 10
	}
	shown := 0
	maxShown := m.height - 12
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

	// Overall progress by bytes on its own line, stats (with speed and ETA)
	// on the next so nothing gets truncated on narrow terminals.
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Overall  %s\n", progressBar(m.width-14, int(bytesWritten), int(m.copyBytes))))
	stats := fmt.Sprintf("%d/%d files · %s / %s",
		m.copyDone, m.copyTotal, fmtBytes(bytesWritten), fmtBytes(m.copyBytes))
	if elapsed := time.Since(m.batchStart); elapsed > 2*time.Second && bytesWritten > 0 {
		rate := float64(bytesWritten) / elapsed.Seconds()
		stats += " · " + fmtBytes(int64(rate)) + "/s"
		if remaining := m.copyBytes - bytesWritten; remaining > 0 && !m.cancelling && rate > 0 {
			stats += " · ETA " + fmtDuration(time.Duration(float64(remaining)/rate*float64(time.Second)))
		}
	}
	sb.WriteString("           " + styleDim.Render(stats) + "\n")

	if m.cancelling {
		sb.WriteString(styleWarn.Render("\n  Cancelling — files in progress will finish; queued files are skipped.") + "\n")
	}
	if m.failures > 0 {
		sb.WriteString(styleErr.Render(fmt.Sprintf("\n  %d file(s) failed so far.", m.failures)) + "\n")
	}

	hint := "[q/esc] cancel after current files  [ctrl+c] force quit"
	if m.cancelling {
		hint = "cancelling…  [ctrl+c] force quit"
	}
	return m.screenFrame(title, sb.String(), hint)
}

// renderVerifyProgress renders the live verify view: overall bar + issue list.
func (m *Model) renderVerifyProgress() string {
	var sb strings.Builder
	sb.WriteString("\n")

	if m.verifyTotal == 0 {
		sb.WriteString(styleDim.Render("  Scanning files…") + "\n")
		return sb.String()
	}

	bar := progressBar(m.width-32, m.verifyDone, m.verifyTotal)
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
	sb.WriteString("\n" + styleOK.Render("  Done!") + "\n\n")
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
	return m.screenFrame("camera-backup", sb.String(), "[r] rescan  [e] errors  [q] quit")
}

// renderErrors renders the error summary screen (copy failures and verify issues).
func (m *Model) renderErrors() string {
	var sb strings.Builder
	sb.WriteString("\n")

	if len(m.failedFiles) == 0 && len(m.verifyIssues) == 0 {
		sb.WriteString(styleDim.Render("  No errors recorded.\n"))
	}
	for _, fp := range m.failedFiles {
		name := truncate(filepath.Base(fp.RelPath), 40)
		errMsg := ""
		if fp.Err != nil {
			errMsg = fp.Err.Error()
		}
		sb.WriteString(styleErr.Render(fmt.Sprintf("  %-40s  %s", name, errMsg)) + "\n")
	}
	for _, r := range m.verifyIssues {
		name := truncate(filepath.Base(r.RelPath), 40)
		sb.WriteString(styleWarn.Render(fmt.Sprintf("  %-40s  %s", name, strings.Join(r.Issues, ", "))) + "\n")
	}

	return m.screenFrame("Error Summary", sb.String(), "[Esc/q] back")
}
