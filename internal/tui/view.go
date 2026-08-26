package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Eric-Eklund/camera-backup/internal/devices"
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

	// Show the visible range in the panel title when the tree overflows.
	treeTitle := "Files"
	if total := len(m.visible); total > midH-2 {
		start, end := m.treeWindow(midH - 2)
		treeTitle = fmt.Sprintf("Files %d–%d/%d", start+1, end, total)
	}

	mid := panel(treeTitle, m.renderTreePanel(treeW-2, midH-2), treeW, midH)
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

// renderDeviceHeader renders the compact device status: ✔ Source · ✔ SSD 412 GB · ✘ NAS.
// Devices with split photo/video roots get a partial marker (⚠ SSD ✔P ✘V)
// when only one root is mounted. In direct mode the SSD is left out — it takes
// no part in the copy — and the arrow spells out where files are going.
func (m *Model) renderDeviceHeader() string {
	if m.status == nil {
		return ""
	}
	r := m.status
	parts := []string{deviceBadge("Source", r.SourceAvail, r.SourceFree)}
	if m.cfg.SSDInUse() {
		parts = append(parts, dualBadge("SSD", r.SSDPhotosAvail, r.SSDVideosAvail, r.SSDPhotosFree, r.SSDVideosFree))
	}
	if m.cfg.NASConfigured() {
		parts = append(parts, dualBadge("NAS", r.NASPhotosAvail, r.NASVideosAvail, r.NASPhotosFree, r.NASVideosFree))
	}
	joiner := styleDim.Render(" · ")
	if m.cfg.DirectToNAS {
		joiner = styleDim.Render(" → ")
	}
	return strings.Join(parts, joiner) + " "
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

// dualBadge renders a device with photos/videos roots: a single badge when
// both roots share availability, a warning with per-root markers otherwise.
func dualBadge(name string, pAvail, vAvail bool, pFree, vFree int64) string {
	if pAvail == vAvail {
		return deviceBadge(name, pAvail, minFree(pFree, vFree))
	}
	p, v := styleErr.Render("✘P"), styleErr.Render("✘V")
	free := vFree
	if pAvail {
		p = styleOK.Render("✔P")
		free = pFree
	}
	if vAvail {
		v = styleOK.Render("✔V")
	}
	s := styleWarn.Render("⚠ "+name) + " " + p + v
	if free > 0 {
		s += styleDim.Render(" " + fmtBytes(free))
	}
	return s
}

// minFree returns the smaller known free-space value (unavailable roots are -1).
func minFree(a, b int64) int64 {
	switch {
	case a < 0:
		return b
	case b < 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

func (m *Model) renderStatusBar() string {
	copyHint := "[y] copy"
	if m.cfg.DirectToNAS {
		copyHint = "[y] dump → NAS"
	}
	hints := "[tab] tabs  [hjkl] move  [enter] expand/preview  [space] select  [g] grid  " +
		copyHint + "  [v] verify  [d] devices  [c] settings  [?] help  [q] quit"
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

// treeWindow returns the [start, end) range of visible tree rows shown in a
// panel of inner height h, keeping the cursor centred when the tree overflows.
func (m *Model) treeWindow(h int) (start, end int) {
	total := len(m.visible)
	if m.cursor >= h {
		start = m.cursor - h/2
	}
	if start+h > total {
		start = total - h
	}
	if start < 0 {
		start = 0
	}
	end = start + h
	if end > total {
		end = total
	}
	return start, end
}

func (m *Model) renderTreePanel(w, h int) string {
	if w <= 0 {
		return ""
	}
	if len(m.visible) == 0 {
		return styleDim.Render("  (no files)")
	}

	start, end := m.treeWindow(h)
	window := make([]string, 0, h)
	for i := start; i < end; i++ {
		window = append(window, m.renderNode(m.visible[i], w, i == m.cursor))
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
		// The SSD marker is dropped in direct mode — the copy never touches it.
		ssdIcon := ""
		if m.cfg.SSDInUse() {
			ssdIcon = styleErr.Render("✗SSD")
			if onSSD {
				ssdIcon = styleOK.Render("✓SSD")
			}
		}
		nasIcon := ""
		if m.status != nil && m.status.NASAvail() {
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
		if ssdIcon == "" {
			nameW = w - 24 // no ✓SSD column to leave room for
		}
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
	cat := m.cfg.Category(f.RelPath)
	key := f.DestKey()
	return m.ssdKeys[cat][key], m.nasKeys[cat][key]
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

// noPreviewNote explains an empty preview area. A missing exiftool is by far
// the most common reason on a fresh machine, and it is worth naming.
func (m *Model) noPreviewNote() string {
	if m.rawToolMissing {
		return "[no preview — install exiftool for RAW files]"
	}
	return "[no preview]"
}

func (m *Model) renderFileDetail(f *scan.FileInfo, w, h int) []string {
	var lines []string
	add := func(s string) {
		lines = append(lines, lipgloss.NewStyle().Width(w).Render(ansi.Truncate(s, w, "…")))
	}

	add(styleHeader.Render(filepath.Base(f.RelPath)))
	add(styleDetailLabel.Render("Size:  ") + styleDetailValue.Render(fmtBytes(f.Size)))
	// Date/Time show the capture time, since that is what decides the
	// destination directory. "(file date)" marks a fallback to the modtime, so
	// an unexpected date is traceable to metadata the file does not carry.
	taken := f.DateTaken()
	date := taken.Format("2006-01-02")
	if f.CaptureTime.IsZero() {
		date += styleDim.Render(" (file date)")
	}
	add(styleDetailLabel.Render("Date:  ") + styleDetailValue.Render(date))
	add(styleDetailLabel.Render("Time:  ") + styleDetailValue.Render(taken.Format("15:04:05")))
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
				add(styleDim.Render("  " + m.noPreviewNote()))
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

	// Mini-grid of thumbnails, laid out in columns and limited to the rows
	// that actually fit in the remaining panel height.
	const cols, thumbH = 3, 4
	thumbW := (w - 2) / cols
	if thumbW < 4 {
		return lines
	}
	maxPrev := (h - len(lines)) / thumbH * cols
	if maxPrev > len(files) {
		maxPrev = len(files)
	}
	for i := 0; i < maxPrev; i += cols {
		rowEnd := i + cols
		if rowEnd > maxPrev {
			rowEnd = maxPrev
		}
		cells := make([]string, 0, cols)
		for _, f := range files[i:rowEnd] {
			if img, ok := m.thumbCache[f.AbsPath]; ok && img != nil {
				cells = append(cells, strings.TrimRight(preview.BlockArt(img, thumbW, thumbH), "\n"))
			} else {
				cells = append(cells, strings.TrimRight(
					strings.Repeat(styleDim.Render(strings.Repeat("░", thumbW))+"\n", thumbH), "\n"))
			}
		}
		row := lipgloss.JoinHorizontal(lipgloss.Top, cells...)
		lines = append(lines, strings.Split(row, "\n")...)
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
		content = styleDim.Render("\n\n  " + m.noPreviewNote())
	default:
		content = styleDim.Render("\n\n  Loading…")
	}

	meta := styleDim.Render(fmt.Sprintf(" %s · %s · %d/%d",
		fmtBytes(f.Size), f.DateTaken().Format("2006-01-02 15:04"), m.gridCursor+1, len(files)))

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
	case modeDirect:
		title = "Direct: Source → NAS (with verification)"
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

// renderSettings renders the settings screen: one row per config key, with a
// live ✔/✘ for every path so a mistyped mount point is obvious immediately.
func (m *Model) renderSettings() string {
	f := m.settings
	if f == nil {
		return m.screenFrame("Settings", "\n  Settings unavailable.", "[esc] back")
	}

	labelW := 20
	markerW := 12
	// Value column: whatever is left inside the frame after the label, marker
	// and the padding between them.
	valueW := m.width - labelW - markerW - 8
	if valueW < 12 {
		valueW = 12
	}

	var sb strings.Builder
	sb.WriteString("\n")
	for i, fl := range f.fields {
		focused := i == f.cursor
		editing := focused && f.editing

		cursorMark := "  "
		if focused {
			cursorMark = styleWarn.Render("▶ ")
		}

		label := fmt.Sprintf("%-*s", labelW, truncate(fl.label, labelW))
		if focused {
			label = styleFieldLabelFocus.Render(label)
		} else {
			label = styleFieldLabel.Render(label)
		}

		// Probe the text being typed, not the last accepted value, so the ✔/✘
		// tracks the keystrokes.
		liveValue := fl.value
		if editing {
			liveValue = f.editor.String()
		}

		var value string
		switch {
		case editing:
			value = f.editor.render(valueW)
		case fl.kind == fieldBool:
			value = renderToggle(fl.value)
		case fl.value == "":
			value = styleDim.Render("(not set)")
		default:
			value = styleFieldValue.Render(ansi.Truncate(fl.value, valueW, "…"))
		}
		pad := valueW - lipgloss.Width(value)
		if pad < 1 {
			pad = 1
		}

		sb.WriteString("  " + cursorMark + label + "  " + value + strings.Repeat(" ", pad) + fl.markerFor(liveValue) + "\n")
	}

	// Footer: the focused key's TOML name and hint, then status.
	focused := f.fields[f.cursor]
	sb.WriteString("\n  " + styleDim.Render(focused.key))
	if focused.hint != "" {
		sb.WriteString(styleDim.Render(" — " + focused.hint))
	}
	sb.WriteString("\n")

	switch {
	case f.err != "":
		sb.WriteString("\n  " + styleErr.Render("✘ "+f.err) + "\n")
	case f.notice != "":
		sb.WriteString("\n  " + styleOK.Render("✔ "+f.notice) + "\n")
	case f.confirmExit:
		sb.WriteString("\n  " + styleWarn.Render("Unsaved changes — [s] save, or [esc] again to discard") + "\n")
	case f.dirty:
		sb.WriteString("\n  " + styleWarn.Render("Unsaved changes") + "\n")
	default:
		sb.WriteString("\n  " + styleDim.Render(f.configPath) + "\n")
	}

	title := "Settings"
	if f.dirty {
		title = "Settings •"
	}
	hint := "[j/k] move  [enter] edit/toggle  [d] devices  [s] save  [r] reload  [esc] back"
	if f.editing {
		hint = "[enter] accept  [esc] cancel  [ctrl+u] clear  [←→] move  typing edits the value"
	}
	return m.screenFrame(title, sb.String(), hint)
}

// renderToggle draws a boolean as a checkbox so its state reads at a glance.
func renderToggle(value string) string {
	if value == boolOn {
		return styleOK.Render("[✔] on")
	}
	return styleDim.Render("[ ] off")
}

// renderDevices renders the device picker: every mounted filesystem that could
// hold media, with the one in use marked. Removable devices and anything
// carrying a DCIM directory sort first, so the card just plugged in is the row
// the cursor lands on.
func (m *Model) renderDevices() string {
	p := m.picker
	if p == nil {
		return m.screenFrame("Devices", "\n  No device list.", "[esc] back")
	}

	title := "Devices"
	hint := "[j/k] move  [enter] use as source  [s] save to config.toml  [r] refresh  [esc] back"
	if p.mode == pickerField {
		title = "Devices → " + p.fieldLabel
		hint = "[j/k] move  [enter] use this path  [r] refresh  [esc] back"
	}

	var sb strings.Builder
	sb.WriteString("\n")

	switch {
	case p.loading:
		sb.WriteString("  " + styleDim.Render("Scanning mounted devices…") + "\n")
	case p.err != "":
		sb.WriteString("  " + styleErr.Render("✘ "+p.err) + "\n")
	case len(p.devs) == 0:
		sb.WriteString("  " + styleWarn.Render("No usable devices found.") + "\n")
		sb.WriteString("  " + styleDim.Render("Plug in a card reader or drive, then press [r].") + "\n")
	default:
		sb.WriteString(m.renderDeviceRows(p))
	}

	if d, ok := p.current(); ok && !p.loading {
		sb.WriteString("\n  " + styleDim.Render(d.Path))
		if d.Node != "" {
			sb.WriteString(styleDim.Render("  ← " + d.Node + " (" + d.FSType + ")"))
		}
		sb.WriteString("\n")
	}

	switch {
	case p.err != "" && len(p.devs) > 0:
		sb.WriteString("\n  " + styleErr.Render("✘ "+p.err) + "\n")
	case p.notice != "":
		sb.WriteString("\n  " + styleOK.Render("✔ "+p.notice) + "\n")
	case p.mode == pickerSwap && !p.loading:
		// The long form does not fit an 80-column terminal, where the panel
		// would clip it mid-sentence.
		note := "[enter] use for this session · [s] also save to config.toml"
		if m.width >= 100 {
			note = "[enter] uses the device for this session and rescans against " +
				destinationsInUse(m.cfg) + " · [s] also writes it to config.toml"
		}
		sb.WriteString("\n  " + styleDim.Render(note) + "\n")
	}

	return m.screenFrame(title, sb.String(), hint)
}

// renderDeviceRows lays the device list out in fixed columns, giving the path
// whatever width is left — at 80 columns the name, kind and free space still
// have to fit.
func (m *Model) renderDeviceRows(p *devicePicker) string {
	const (
		nameW = 18
		kindW = 11
		freeW = 15
	)
	pathW := m.width - nameW - kindW - freeW - 14
	if pathW < 10 {
		pathW = 10
	}

	header := fmt.Sprintf("      %-*s %-*s %-*s %s",
		nameW, "DEVICE", kindW, "TYPE", pathW, "MOUNT POINT", "FREE")
	var sb strings.Builder
	sb.WriteString("  " + styleDim.Render(header) + "\n")

	for i, d := range p.devs {
		cursorMark := "  "
		if i == p.cursor {
			cursorMark = styleWarn.Render("▶ ")
		}
		// ● is the device the next scan will read — either the one in use, or
		// the row about to replace it.
		active := styleDim.Render("○ ")
		if d.Path == p.active {
			active = styleOK.Render("● ")
		}

		name := truncate(d.Name(), nameW)
		if d.HasDCIM {
			// A DCIM directory means a camera wrote this card.
			name = truncate(d.Name(), nameW-5) + " DCIM"
		}
		nameCol := fmt.Sprintf("%-*s", nameW, name)
		if i == p.cursor {
			nameCol = styleFieldLabelFocus.Render(nameCol)
		} else {
			nameCol = styleFieldValue.Render(nameCol)
		}

		free := styleDim.Render(fmt.Sprintf("%*s", freeW, "—"))
		if d.TotalBytes > 0 {
			free = styleDim.Render(fmt.Sprintf("%*s", freeW,
				devices.FormatBytes(d.FreeBytes)+" free"))
		}

		sb.WriteString(fmt.Sprintf("  %s%s%s %s %s %s\n",
			cursorMark, active, nameCol,
			deviceKindLabel(d.Kind, kindW),
			styleDim.Render(fmt.Sprintf("%-*s", pathW, truncate(d.Path, pathW))),
			free))
	}
	return sb.String()
}

// deviceKindLabel colours how a device is attached: what the user just plugged
// in should stand out from the disk the system booted off.
func deviceKindLabel(k devices.Kind, w int) string {
	text := fmt.Sprintf("%-*s", w, k.String())
	switch k {
	case devices.KindRemovable:
		return styleOK.Render(text)
	case devices.KindNetwork:
		return styleWarn.Render(text)
	}
	return styleDim.Render(text)
}

// renderHelp renders the keybinding reference screen.
func (m *Model) renderHelp() string {
	blocks := m.helpBlocks()
	avail := m.helpHeight()
	lines, off, more := helpWindow(helpBody(blocks, avail), m.helpOffset, avail)
	hint := "[Esc/q/?] back"
	if more || off > 0 {
		hint = "[j/k] scroll  " + hint
	}
	return m.screenFrame("Help", "\n"+strings.Join(lines, "\n"), hint)
}

// helpHeight is how many body lines the help screen can show: the frame takes
// the top and bottom border and the status bar, and the body opens with a
// blank line.
func (m *Model) helpHeight() int {
	h := m.height - 4
	if h < 4 {
		h = 4
	}
	return h
}

// helpBlocks builds the keybinding reference, one block per section.
func (m *Model) helpBlocks() [][]string {
	type binding struct{ keys, desc string }
	type section struct {
		title    string
		bindings []binding
	}
	copyDesc := "copy — selected files, or everything missing"
	if m.cfg.DirectToNAS {
		copyDesc = "dump straight to the NAS (verified, local SSD bypassed)"
	}
	sections := []section{
		{"Navigation", []binding{
			{"j/k or ↑/↓", "move cursor"},
			{"l/→", "open group · step into it · preview file"},
			{"h/←", "close group · step out to the one above"},
			{"enter", "expand/collapse group · preview file"},
			{"tab / shift+tab", "next / previous tab"},
			{"g", "thumbnail grid for the focused date"},
		}},
		{"Selection", []binding{
			{"space", "select/deselect file or group"},
			{"a", "select/deselect everything in the tab"},
		}},
		{"Actions", []binding{
			{"y", copyDesc},
			{"v", "verify checksums (camera vs SSD vs NAS)"},
			{"d", "devices — pick the card or drive to back up from"},
			{"c", "settings — edit paths and options, saved to config.toml"},
		}},
		{"Device screen", []binding{
			{"enter", "use as the source now — rescans against SSD/NAS"},
			{"s", "also save it to config.toml as source"},
			{"r", "rescan for mounted devices"},
			{"esc", "back"},
		}},
		{"Settings screen", []binding{
			{"enter / space", "edit a value · toggle · cycle choices"},
			{"s · r", "save to config.toml · reload from it"},
			{"d", "pick a source path from the device list"},
			{"ctrl+u", "clear the field while editing"},
			{"esc", "cancel the edit, or leave the screen"},
		}},
		{"Grid & preview", []binding{
			{"←→↑↓ / hjkl", "navigate thumbnails"},
			{"enter/p", "full-size preview"},
			{"space · y", "select · copy from the grid"},
			{"esc", "back"},
		}},
		{"While copying", []binding{
			{"q/esc", "cancel — files in progress finish, the queue stops"},
			{"ctrl+c", "force quit"},
		}},
		{"General", []binding{
			{"?", "toggle this help"},
			{"q", "quit (from the main screen)"},
			{"ctrl+c", "quit from anywhere"},
		}},
	}

	blocks := make([][]string, 0, len(sections))
	for _, sec := range sections {
		block := []string{"  " + styleHeader.Render(sec.title)}
		for _, b := range sec.bindings {
			block = append(block, fmt.Sprintf("    %s  %s",
				styleOK.Render(fmt.Sprintf("%-16s", b.keys)), styleDim.Render(b.desc)))
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// helpBody lays the help sections out for the available height: spaced when
// there is room, otherwise packed together. What still does not fit is reached
// by scrolling — the screen has no room to lose a keybinding silently.
func helpBody(blocks [][]string, height int) []string {
	if lines := flattenHelp(blocks, true); len(lines) <= height {
		return lines
	}
	return flattenHelp(blocks, false)
}

// flattenHelp joins section blocks into lines, optionally with a blank line
// between sections.
func flattenHelp(blocks [][]string, spaced bool) []string {
	var out []string
	for i, b := range blocks {
		if spaced && i > 0 {
			out = append(out, "")
		}
		out = append(out, b...)
	}
	return out
}

// helpWindow clamps a scroll offset to what the screen can show, returning the
// visible slice and whether anything is left above or below.
func helpWindow(lines []string, offset, height int) (window []string, off int, more bool) {
	if height < 1 {
		height = 1
	}
	if max := len(lines) - height; offset > max {
		offset = max
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + height
	if end > len(lines) {
		end = len(lines)
	}
	return lines[offset:end], offset, end < len(lines)
}

func (m *Model) renderDone() string {
	var sb strings.Builder
	sb.WriteString("\n" + styleOK.Render("  Done!") + "\n\n")
	// doneMsg can be several lines (an unmounted destination is listed under the
	// result), so indent each one and keep it inside the frame.
	for _, line := range strings.Split(m.doneMsg, "\n") {
		sb.WriteString("  " + ansi.Truncate(line, m.width-6, "…") + "\n")
	}

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
