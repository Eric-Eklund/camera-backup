package tui

import (
	"context"
	"fmt"
	"image"
	"log"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/copyop"
	"github.com/Eric-Eklund/camera-backup/internal/preview"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/status"
	"github.com/Eric-Eklund/camera-backup/internal/verify"
)

type Screen int

const (
	screenLoading  Screen = iota
	screenMain            // three-panel layout
	screenGrid            // full-screen thumbnail grid for a date group
	screenPreview         // full-screen single image
	screenProgress        // copy/sync/verify in progress
	screenConfirm         // between phase 1 done and phase 2 start
	screenDone            // completed
	screenErrors          // error summary
	screenHelp            // keybinding reference
)

type progressMode int

const (
	modePhase1 progressMode = iota // Camera→SSD verify
	modePhase2                     // SSD→NAS fast
	modeSync                       // SSD→NAS sync (no camera)
	modeVerify
)

// treeNode is one row in the flattened visible list.
type treeNode struct {
	level    int    // 0=year  1=month  2=day  3=file
	year     string // "2026"
	month    string // "2026-03"
	day      string // "2026-03-24"
	fileIdx  int    // index into day's file slice (level 3 only)
	expanded bool   // for level 0–2
}

// treeFiles organises scan.FileInfo by year→month→day.
type treeFiles map[string]map[string]map[string][]scan.FileInfo

// Model is the root bubbletea model.
type Model struct {
	cfg    *config.Config
	logger *log.Logger
	p      *tea.Program

	screen        Screen
	width, height int

	// status scan
	status    *status.StatusResult
	statusMsg string // shown in status bar while scanning
	// Lowercased dest relpaths present on each device, keyed by category —
	// lookups go against the file's designated root, matching how the
	// missing-file computation works.
	ssdKeys map[string]map[string]bool
	nasKeys map[string]map[string]bool

	// tabs: e.g. ["All (312)", "Missing on SSD (12)", "Missing on NAS (47)"]
	tabs      []string
	tabKeys   []tabKey // parallel to tabs; identifies what each tab shows
	activeTab int

	// tree
	allFiles   []scan.FileInfo // files shown in current tab
	tree       treeFiles
	yearOrder  []string                       // sorted years
	monthOrder map[string][]string            // year → sorted months
	dayOrder   map[string]map[string][]string // year → month → sorted days
	expanded   map[string]bool                // "year", "year/month", "year/month/day" → expanded
	visible    []treeNode
	cursor     int
	selected   map[string]bool // absPath → selected; empty = operate on all

	// detail / preview
	thumbCache   map[string]image.Image // absPath → decoded image (nil = unsupported)
	loadingThumb map[string]bool        // absPath → thumbnail load in flight
	fullCache    map[string]image.Image // absPath → full-size preview (nil = unsupported)
	loadingFull  map[string]bool        // absPath → full image load in flight
	kitty        bool                   // terminal supports Kitty Graphics Protocol
	prevScreen   Screen
	helpReturn   Screen // screen to return to when help closes
	gridYear     string
	gridMonth    string
	gridDay      string
	gridCursor   int
	gridOffset   int // first visible thumbnail row (scroll position)

	// copy/verify progress
	progressMode  progressMode
	fileProgress  map[string]copyop.FileProgress
	progressOrder []string             // RelPaths in first-seen order for stable rendering
	fileStart     map[string]time.Time // RelPath → first event time, for speed calc
	copyDone      int
	copyTotal     int
	copyBytes     int64 // total bytes across all tasks in the running batch
	failedFiles   []copyop.FileProgress
	batchStart    time.Time          // when the running batch started, for overall speed/ETA
	cancelBatch   context.CancelFunc // cancels the running batch; nil when idle
	cancelling    bool               // user requested cancel; batch is draining
	skippedNoRoot int                // files skipped because their category root is unmounted

	// verify progress
	verifyIssues []verify.FileResult // files with problems, shown on error screen
	verifyDone   int
	verifyTotal  int

	failures int
	doneMsg  string
	lastErr  error

	// phase2 tasks cached for the confirm screen
	phase2Tasks []copyop.Task
}

type tabKey int

const (
	tabAll tabKey = iota
	tabMissingOnSSD
	tabMissingOnNAS
)

// New creates a new TUI model.
func New(cfg *config.Config, logger *log.Logger) *Model {
	return &Model{
		cfg:          cfg,
		logger:       logger,
		screen:       screenLoading,
		statusMsg:    "Scanning devices…",
		thumbCache:   map[string]image.Image{},
		loadingThumb: map[string]bool{},
		fullCache:    map[string]image.Image{},
		loadingFull:  map[string]bool{},
		kitty:        preview.KittySupported(),
		expanded:     map[string]bool{},
		selected:     map[string]bool{},
		fileProgress: map[string]copyop.FileProgress{},
		fileStart:    map[string]time.Time{},
	}
}

// SetProgram wires the tea.Program back into the model so ops.go can call p.Send().
func (m *Model) SetProgram(p *tea.Program) {
	m.p = p
}

// Init fires the initial status scan and device watcher.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		statusScanCmd(m.cfg, m.logger),
		watchDevicesCmd(m.cfg, m.p),
	)
}

// Update handles all messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		return m.handleKey(msg)

	case statusReadyMsg:
		// Only apply on the loading/main screens — never yank the user out of a
		// running operation, confirm dialog, or preview.
		if m.screen != screenLoading && m.screen != screenMain {
			return m, nil
		}
		if msg.err != nil {
			m.lastErr = msg.err
			m.screen = screenDone
			m.doneMsg = "Error scanning devices: " + msg.err.Error()
			return m, nil
		}
		m.status = msg.result
		m.ssdKeys = map[string]map[string]bool{
			"photos": relPathSet(msg.result.SSDPhotoFiles),
			"videos": relPathSet(msg.result.SSDVideoFiles),
		}
		m.nasKeys = map[string]map[string]bool{
			"photos": relPathSet(msg.result.NASPhotoFiles),
			"videos": relPathSet(msg.result.NASVideoFiles),
		}
		m.buildTabs()
		m.setTab(m.activeTab)
		m.screen = screenMain
		m.statusMsg = ""
		if msg.result.CameraUnstable > 0 {
			m.statusMsg = fmt.Sprintf("%d camera file(s) skipped — possibly still being written; rescan when the card is idle.", msg.result.CameraUnstable)
		}
		return m, m.maybeLoadThumb()

	case deviceChangedMsg:
		// Ignore device events during operations; a fresh scan runs when the
		// user returns to the main screen anyway.
		if m.screen != screenMain && m.screen != screenLoading {
			return m, nil
		}
		m.statusMsg = "Rescanning…"
		return m, statusScanCmd(m.cfg, m.logger)

	case fileProgressMsg:
		fp := msg.p
		if _, seen := m.fileProgress[fp.RelPath]; !seen {
			m.progressOrder = append(m.progressOrder, fp.RelPath)
			m.fileStart[fp.RelPath] = time.Now()
		}
		m.fileProgress[fp.RelPath] = fp
		if fp.Done {
			m.copyDone++
			if fp.Err != nil {
				m.failures++
				m.failedFiles = append(m.failedFiles, fp)
			}
		}

	case phase1DoneMsg:
		m.failures = msg.failures
		m.cancelBatch = nil
		copied := m.copyDone - msg.failures
		if m.cancelling {
			m.screen = screenDone
			m.doneMsg = fmt.Sprintf("Cancelled: %d of %d files copied to SSD (%d skipped).",
				copied, m.copyTotal, m.copyTotal-m.copyDone)
			return m, nil
		}
		if m.status != nil && m.status.NASAvail() {
			// Rescan SSD vs NAS so Phase 2 copies from the SSD (never the
			// camera) and picks up everything Phase 1 just wrote.
			m.statusMsg = "Scanning SSD → NAS…"
			return m, preparePhase2Cmd(m.cfg, m.logger)
		}
		m.screen = screenDone
		m.doneMsg = fmt.Sprintf("Phase 1 complete: %d of %d files copied to SSD.%s",
			copied, m.copyTotal, skippedNote(m.skippedNoRoot))
		return m, nil

	case phase2ReadyMsg:
		m.statusMsg = ""
		m.skippedNoRoot = msg.skipped
		if len(msg.tasks) == 0 {
			m.screen = screenDone
			if msg.skipped > 0 {
				m.doneMsg = fmt.Sprintf("Phase 1 complete — nothing to copy to NAS.%s", skippedNote(msg.skipped))
			} else {
				m.doneMsg = "Phase 1 complete — NAS is already up to date."
			}
			return m, nil
		}
		m.phase2Tasks = msg.tasks
		m.screen = screenConfirm
		return m, nil

	case copyDoneMsg:
		m.failures += msg.failures
		m.cancelBatch = nil
		m.screen = screenDone
		switch {
		case m.cancelling:
			m.doneMsg = fmt.Sprintf("Cancelled: %d of %d files copied (%d skipped).",
				m.copyDone-m.failures, m.copyTotal, m.copyTotal-m.copyDone)
		case m.failures > 0:
			m.doneMsg = fmt.Sprintf("Copy finished: %d of %d files copied, %d failed.%s",
				m.copyDone-m.failures, m.copyTotal, m.failures, skippedNote(m.skippedNoRoot))
		default:
			m.doneMsg = fmt.Sprintf("Copy complete: %d files.%s", m.copyDone, skippedNote(m.skippedNoRoot))
		}
		return m, nil

	case progressTickMsg:
		// Keep ticking while the progress screen is visible; each tick
		// re-renders so per-file speeds and the overall ETA stay live.
		if m.screen == screenProgress {
			return m, progressTickCmd()
		}
		return m, nil

	case verifyFileMsg:
		m.verifyDone = msg.done
		m.verifyTotal = msg.total
		if len(msg.result.Issues) > 0 {
			m.verifyIssues = append(m.verifyIssues, msg.result)
		}

	case verifyDoneMsg:
		m.screen = screenDone
		switch {
		case msg.bad < 0:
			m.doneMsg = "Verify failed to run — check the log."
		case msg.bad == 0:
			m.doneMsg = fmt.Sprintf("All %d files verified OK.", msg.total)
		default:
			m.doneMsg = fmt.Sprintf("%d / %d files have issues.", msg.bad, msg.total)
		}

	case thumbnailMsg:
		if msg.err == nil && msg.img != nil {
			m.thumbCache[msg.file] = msg.img
		} else if msg.err == nil {
			m.thumbCache[msg.file] = nil // unsupported type; don't retry
		}
		delete(m.loadingThumb, msg.file)

	case fullImageMsg:
		if msg.err == nil {
			m.fullCache[msg.file] = msg.img // may be nil = unsupported
		}
		delete(m.loadingFull, msg.file)
		// If this is the file currently on the preview screen, draw it.
		if m.screen == screenPreview && m.kitty && msg.img != nil {
			if f := m.previewFile(); f != nil && f.AbsPath == msg.file {
				return m, m.kittyPreviewCmd(msg.img)
			}
		}
	}

	return m, nil
}

// previewFile returns the file currently shown on the preview screen.
func (m *Model) previewFile() *scan.FileInfo {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	if m.gridCursor < 0 || m.gridCursor >= len(files) {
		return nil
	}
	return &files[m.gridCursor]
}

// kittyPreviewCmd draws img in the preview screen's image area.
func (m *Model) kittyPreviewCmd(img image.Image) tea.Cmd {
	cols := m.width - 4
	rows := m.height - 4
	if cols < 1 || rows < 1 {
		return nil
	}
	return kittyDrawCmd(img, cols, rows, 3, 3)
}

// handleKey processes keyboard input based on the current screen.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenLoading:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case screenMain:
		return m.handleMainKey(msg)

	case screenGrid:
		return m.handleGridKey(msg)

	case screenPreview:
		return m.handlePreviewKey(msg)

	case screenProgress:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q", "esc":
			if m.progressMode == modeVerify {
				// Verify only reads files — quitting mid-run is safe.
				return m, tea.Quit
			}
			// Graceful cancel: files being copied finish, queued files are skipped.
			if m.cancelBatch != nil && !m.cancelling {
				m.cancelling = true
				m.cancelBatch()
			}
		}

	case screenConfirm:
		switch msg.String() {
		case "y", "Y":
			return m.startPhase2()
		case "n", "N", "q", "esc":
			m.screen = screenDone
			m.doneMsg = "Stopped after Phase 1."
		case "ctrl+c":
			return m, tea.Quit
		}

	case screenDone:
		switch msg.String() {
		case "r", "esc", "enter":
			m.screen = screenLoading
			m.statusMsg = "Scanning devices…"
			m.fileProgress = map[string]copyop.FileProgress{}
			m.progressOrder = nil
			m.fileStart = map[string]time.Time{}
			m.failures = 0
			m.failedFiles = nil
			m.verifyIssues = nil
			m.copyDone, m.copyTotal = 0, 0
			return m, statusScanCmd(m.cfg, m.logger)
		case "e":
			if len(m.failedFiles) > 0 || len(m.verifyIssues) > 0 {
				m.screen = screenErrors
			}
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case screenErrors:
		switch msg.String() {
		case "esc", "q":
			m.screen = screenDone
		case "ctrl+c":
			return m, tea.Quit
		}

	case screenHelp:
		switch msg.String() {
		case "esc", "q", "?":
			m.screen = m.helpReturn
		case "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *Model) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "tab":
		m.setTab((m.activeTab + 1) % len(m.tabs))

	case "shift+tab":
		m.setTab((m.activeTab - 1 + len(m.tabs)) % len(m.tabs))

	case "j", "down":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
			return m, m.maybeLoadThumb()
		}

	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			return m, m.maybeLoadThumb()
		}

	case "enter":
		if len(m.visible) == 0 {
			break
		}
		node := m.visible[m.cursor]
		if node.level < 3 {
			// Toggle expand/collapse for year/month/day nodes.
			key := nodeKey(node)
			m.expanded[key] = !m.expanded[key]
			m.rebuildVisible()
		} else {
			// File node: open full preview of this file.
			m.prevScreen = screenMain
			m.gridYear = node.year
			m.gridMonth = node.month
			m.gridDay = node.day
			m.gridCursor = node.fileIdx
			m.screen = screenPreview
			return m, m.loadThumbsForPreview()
		}

	case "g", "G":
		// Enter ScreenGrid for current date group.
		if len(m.visible) == 0 {
			break
		}
		node := m.visible[m.cursor]
		if node.level >= 2 {
			m.prevScreen = screenMain
			m.gridYear = node.year
			m.gridMonth = node.month
			m.gridDay = node.day
			m.gridCursor = 0
			m.gridOffset = 0
			m.statusMsg = ""
			m.screen = screenGrid
			return m, m.loadThumbsForGridWindow()
		}

	case "v", "V":
		return m.startVerify()

	case "y", "Y":
		return m.startCopy()

	case "a":
		m.toggleSelectAll()

	case " ":
		m.toggleSelect()

	case "?":
		m.helpReturn = screenMain
		m.screen = screenHelp
	}
	return m, nil
}

// toggleSelect toggles selection of the focused file, or all files under the
// focused year/month/day group.
func (m *Model) toggleSelect() {
	if len(m.visible) == 0 || m.cursor >= len(m.visible) {
		return
	}
	node := m.visible[m.cursor]
	files := m.nodeFiles(node)
	if len(files) == 0 {
		return
	}
	// If every file under the node is selected, deselect; otherwise select all.
	all := true
	for _, f := range files {
		if !m.selected[f.AbsPath] {
			all = false
			break
		}
	}
	for _, f := range files {
		if all {
			delete(m.selected, f.AbsPath)
		} else {
			m.selected[f.AbsPath] = true
		}
	}
}

// toggleSelectAll selects every file in the current tab, or clears the
// selection if everything is already selected.
func (m *Model) toggleSelectAll() {
	all := true
	for _, f := range m.allFiles {
		if !m.selected[f.AbsPath] {
			all = false
			break
		}
	}
	for _, f := range m.allFiles {
		if all {
			delete(m.selected, f.AbsPath)
		} else {
			m.selected[f.AbsPath] = true
		}
	}
}

// nodeFiles returns the files under a tree node: one file for a file node,
// all files in the group for year/month/day nodes.
func (m *Model) nodeFiles(node treeNode) []scan.FileInfo {
	switch node.level {
	case 3:
		files := m.dayFiles(node.year, node.month, node.day)
		if node.fileIdx < len(files) {
			return files[node.fileIdx : node.fileIdx+1]
		}
		return nil
	case 2:
		return m.dayFiles(node.year, node.month, node.day)
	case 1:
		var out []scan.FileInfo
		for _, day := range m.dayOrder[node.year][node.month] {
			out = append(out, m.dayFiles(node.year, node.month, day)...)
		}
		return out
	default:
		var out []scan.FileInfo
		for _, month := range m.monthOrder[node.year] {
			for _, day := range m.dayOrder[node.year][month] {
				out = append(out, m.dayFiles(node.year, month, day)...)
			}
		}
		return out
	}
}

// selectedIn returns the subset of files that are selected. If the selection
// is empty, all files are returned (no selection = operate on everything).
func (m *Model) selectedIn(files []scan.FileInfo) []scan.FileInfo {
	if len(m.selected) == 0 {
		return files
	}
	var out []scan.FileInfo
	for _, f := range files {
		if f.AbsPath != "" && m.selected[f.AbsPath] {
			out = append(out, f)
		}
	}
	return out
}

func (m *Model) handleGridKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	switch msg.String() {
	case "esc", "q":
		m.screen = screenMain
	case "ctrl+c":
		return m, tea.Quit
	case "p", "enter":
		if m.gridCursor < len(files) {
			m.prevScreen = screenGrid
			m.screen = screenPreview
			return m, m.loadThumbsForPreview()
		}
	case "y", "Y":
		// Copy directly from the grid; selection (if any) applies as usual.
		return m.startCopy()
	case "?":
		m.helpReturn = screenGrid
		m.screen = screenHelp
	case " ":
		if m.gridCursor < len(files) {
			f := files[m.gridCursor]
			if m.selected[f.AbsPath] {
				delete(m.selected, f.AbsPath)
			} else {
				m.selected[f.AbsPath] = true
			}
		}
	case "left", "h":
		if m.gridCursor > 0 {
			m.gridCursor--
			return m, m.gridScrollToCursor()
		}
	case "right", "l":
		if m.gridCursor < len(files)-1 {
			m.gridCursor++
			return m, m.gridScrollToCursor()
		}
	case "up", "k":
		cols := m.gridCols()
		if m.gridCursor >= cols {
			m.gridCursor -= cols
			return m, m.gridScrollToCursor()
		}
	case "down", "j":
		cols := m.gridCols()
		if m.gridCursor+cols < len(files) {
			m.gridCursor += cols
			return m, m.gridScrollToCursor()
		}
	}
	return m, nil
}

// gridVisibleRows returns how many thumbnail rows fit inside the grid frame.
func (m *Model) gridVisibleRows() int {
	rowH := gridThumbH + 1 // thumbnail + label line
	rows := (m.height - 3) / rowH
	if rows < 1 {
		rows = 1
	}
	return rows
}

// gridScrollToCursor scrolls the grid so the cursor row is visible and loads
// thumbnails for the newly visible window.
func (m *Model) gridScrollToCursor() tea.Cmd {
	cols := m.gridCols()
	row := m.gridCursor / cols
	vis := m.gridVisibleRows()
	if row < m.gridOffset {
		m.gridOffset = row
	}
	if row >= m.gridOffset+vis {
		m.gridOffset = row - vis + 1
	}
	return m.loadThumbsForGridWindow()
}

// loadThumbsForGridWindow fires thumbnail loads for the files in the visible
// grid window (plus one lookahead row). Cached files are skipped, so scrolling
// through a large day loads thumbnails incrementally instead of stopping at a
// fixed cap.
func (m *Model) loadThumbsForGridWindow() tea.Cmd {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	cols := m.gridCols()
	start := m.gridOffset * cols
	end := (m.gridOffset + m.gridVisibleRows() + 1) * cols
	if start < 0 {
		start = 0
	}
	if end > len(files) {
		end = len(files)
	}
	var cmds []tea.Cmd
	for i := start; i < end; i++ {
		if cmd := m.loadThumb(files[i].AbsPath); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) handlePreviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	switch msg.String() {
	case "esc", "q":
		if m.kitty {
			preview.KittyClear()
		}
		if m.prevScreen == screenGrid {
			m.screen = screenGrid
		} else {
			m.screen = screenMain
		}
	case "ctrl+c":
		if m.kitty {
			preview.KittyClear()
		}
		return m, tea.Quit
	case "left", "h":
		if m.gridCursor > 0 {
			m.gridCursor--
			return m, m.loadThumbsForPreview()
		}
	case "right", "l":
		if m.gridCursor < len(files)-1 {
			m.gridCursor++
			return m, m.loadThumbsForPreview()
		}
	}
	return m, nil
}

// loadThumbsForPreview loads the focused preview file (full size) plus
// thumbnails for its neighbours, and draws via Kitty when already cached.
func (m *Model) loadThumbsForPreview() tea.Cmd {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	var cmds []tea.Cmd
	for _, idx := range []int{m.gridCursor, m.gridCursor - 1, m.gridCursor + 1} {
		if idx < 0 || idx >= len(files) {
			continue
		}
		if cmd := m.loadThumb(files[idx].AbsPath); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// Full-resolution image for the focused file.
	if f := m.previewFile(); f != nil {
		if img, cached := m.fullCache[f.AbsPath]; cached {
			if m.kitty && img != nil {
				cmds = append(cmds, m.kittyPreviewCmd(img))
			}
		} else if !m.loadingFull[f.AbsPath] {
			m.loadingFull[f.AbsPath] = true
			cmds = append(cmds, fullImageCmd(f.AbsPath))
		}
	}
	return tea.Batch(cmds...)
}

// loadThumb returns a thumbnailCmd for absPath unless already cached or loading.
func (m *Model) loadThumb(absPath string) tea.Cmd {
	if _, cached := m.thumbCache[absPath]; cached {
		return nil
	}
	if m.loadingThumb[absPath] {
		return nil
	}
	m.loadingThumb[absPath] = true
	return thumbnailCmd(absPath)
}

// buildTabs constructs the tab list based on device availability.
func (m *Model) buildTabs() {
	m.tabs = nil
	m.tabKeys = nil
	r := m.status

	if r.SourceAvail || r.SSDAvail() {
		count := len(r.CameraFiles)
		if !r.SourceAvail {
			count = len(r.SSDFiles)
		}
		m.tabs = append(m.tabs, fmt.Sprintf("All (%d)", count))
		m.tabKeys = append(m.tabKeys, tabAll)
	}
	if r.SourceAvail && r.SSDAvail() {
		m.tabs = append(m.tabs, fmt.Sprintf("Missing on SSD (%d)", len(r.MissingOnSSD)))
		m.tabKeys = append(m.tabKeys, tabMissingOnSSD)
	}
	if r.NASAvail() {
		m.tabs = append(m.tabs, fmt.Sprintf("Missing on NAS (%d)", len(r.MissingOnNAS)))
		m.tabKeys = append(m.tabKeys, tabMissingOnNAS)
	}

	if m.activeTab >= len(m.tabs) {
		m.activeTab = 0
	}
}

func (m *Model) setTab(idx int) {
	if idx < 0 || idx >= len(m.tabs) {
		return
	}
	m.activeTab = idx
	m.allFiles = m.filesForTab(m.tabKeys[idx])
	m.buildTree()
	m.rebuildVisible()
}

func (m *Model) filesForTab(k tabKey) []scan.FileInfo {
	if m.status == nil {
		return nil
	}
	switch k {
	case tabMissingOnSSD:
		return m.status.MissingOnSSD
	case tabMissingOnNAS:
		return m.status.MissingOnNAS
	default:
		if m.status.SourceAvail {
			return m.status.CameraFiles
		}
		return m.status.SSDFiles
	}
}

// buildTree organises m.allFiles into the year→month→day→files hierarchy.
func (m *Model) buildTree() {
	m.tree = treeFiles{}
	m.yearOrder = nil
	m.monthOrder = map[string][]string{}
	m.dayOrder = map[string]map[string][]string{}

	for _, f := range m.allFiles {
		year := f.ModTime.Format("2006")
		month := f.ModTime.Format("2006-01")
		day := f.ModTime.Format("2006-01-02")

		if m.tree[year] == nil {
			m.tree[year] = map[string]map[string][]scan.FileInfo{}
		}
		if m.tree[year][month] == nil {
			m.tree[year][month] = map[string][]scan.FileInfo{}
		}
		m.tree[year][month][day] = append(m.tree[year][month][day], f)
	}

	for year := range m.tree {
		m.yearOrder = append(m.yearOrder, year)
		for month := range m.tree[year] {
			m.monthOrder[year] = append(m.monthOrder[year], month)
			if m.dayOrder[year] == nil {
				m.dayOrder[year] = map[string][]string{}
			}
			for day := range m.tree[year][month] {
				m.dayOrder[year][month] = append(m.dayOrder[year][month], day)
			}
			sort.Strings(m.dayOrder[year][month])
		}
		sort.Strings(m.monthOrder[year])
	}
	sort.Strings(m.yearOrder)

	// Default: expand all nodes.
	for year := range m.tree {
		m.expanded[year] = true
		for month := range m.tree[year] {
			m.expanded[year+"/"+month] = true
			for day := range m.tree[year][month] {
				m.expanded[year+"/"+month+"/"+day] = true
			}
		}
	}
}

// rebuildVisible rebuilds the flat visible list from the tree + expanded state.
func (m *Model) rebuildVisible() {
	m.visible = nil
	for _, year := range m.yearOrder {
		yearExp := m.expanded[year]
		m.visible = append(m.visible, treeNode{level: 0, year: year, expanded: yearExp})
		if !yearExp {
			continue
		}
		for _, month := range m.monthOrder[year] {
			monthKey := year + "/" + month
			monthExp := m.expanded[monthKey]
			m.visible = append(m.visible, treeNode{level: 1, year: year, month: month, expanded: monthExp})
			if !monthExp {
				continue
			}
			for _, day := range m.dayOrder[year][month] {
				dayKey := year + "/" + month + "/" + day
				dayExp := m.expanded[dayKey]
				m.visible = append(m.visible, treeNode{level: 2, year: year, month: month, day: day, expanded: dayExp})
				if !dayExp {
					continue
				}
				files := m.tree[year][month][day]
				for i := range files {
					m.visible = append(m.visible, treeNode{level: 3, year: year, month: month, day: day, fileIdx: i})
				}
			}
		}
	}

	// Clamp cursor.
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func nodeKey(n treeNode) string {
	switch n.level {
	case 0:
		return n.year
	case 1:
		return n.year + "/" + n.month
	default:
		return n.year + "/" + n.month + "/" + n.day
	}
}

// dayFiles returns all files for the given year/month/day (safe if tree is nil).
func (m *Model) dayFiles(year, month, day string) []scan.FileInfo {
	if m.tree == nil {
		return nil
	}
	if ym, ok := m.tree[year]; ok {
		if ymd, ok := ym[month]; ok {
			return ymd[day]
		}
	}
	return nil
}

// maybeLoadThumb fires thumbnail loads for the focused node: the file itself
// on a file row, or the first few files of a date group so the Info panel's
// mini-grid can render.
func (m *Model) maybeLoadThumb() tea.Cmd {
	if len(m.visible) == 0 || m.cursor >= len(m.visible) {
		return nil
	}
	node := m.visible[m.cursor]
	switch node.level {
	case 3:
		f := m.tree[node.year][node.month][node.day][node.fileIdx]
		return m.loadThumb(f.AbsPath)
	case 2:
		const maxLoads = 9 // generous upper bound for the mini-grid
		var cmds []tea.Cmd
		for i, f := range m.dayFiles(node.year, node.month, node.day) {
			if i >= maxLoads {
				break
			}
			if cmd := m.loadThumb(f.AbsPath); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return tea.Batch(cmds...)
	}
	return nil
}

// resetProgress clears all per-batch progress state.
func (m *Model) resetProgress(total int, totalBytes int64) {
	m.copyTotal = total
	m.copyDone = 0
	m.copyBytes = totalBytes
	m.fileProgress = map[string]copyop.FileProgress{}
	m.progressOrder = nil
	m.fileStart = map[string]time.Time{}
	m.batchStart = time.Now()
	m.cancelling = false
}

// startCopy determines which phase(s) to run and launches them.
// If a selection is active, only selected files are copied (camera→SSD and
// sync modes); Phase 2 after a camera copy always pushes everything missing.
// Files whose category root is unmounted are skipped and reported.
func (m *Model) startCopy() (tea.Model, tea.Cmd) {
	if m.status == nil {
		return m, nil
	}
	r := m.status

	m.failures = 0
	m.failedFiles = nil
	m.skippedNoRoot = 0

	switch {
	case r.SourceAvail && r.SSDAvail():
		// Phase 1: Camera→SSD (with verify); Phase 2: SSD→NAS (if available).
		missing := m.selectedIn(r.MissingOnSSD)
		if len(m.selected) > 0 && len(missing) == 0 {
			m.statusMsg = "No selected files need copying to SSD."
			return m, nil
		}
		tasks, skipped := buildPhase1Tasks(missing, m.cfg, r)
		m.skippedNoRoot = skipped
		if len(tasks) == 0 {
			if skipped > 0 {
				m.statusMsg = fmt.Sprintf("%d file(s) skipped — category root not mounted.", skipped)
				return m, nil
			}
			if !r.NASAvail() {
				m.statusMsg = "SSD is already up to date."
				return m, nil
			}
			// Nothing for Phase 1 — go straight to the Phase 2 scan.
			m.statusMsg = "Scanning SSD → NAS…"
			return m, preparePhase2Cmd(m.cfg, m.logger)
		}
		if err := copyop.CheckSpace(tasks); err != nil {
			m.statusMsg = err.Error()
			return m, nil
		}
		m.resetProgress(len(tasks), copyop.TotalSize(tasks))
		m.progressMode = modePhase1
		m.screen = screenProgress
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelBatch = cancel
		events := make(chan copyop.FileProgress, 64)
		result := make(chan int, 1)
		return m, tea.Batch(
			runBatchCmd(ctx, tasks, m.logger, true, m.cfg.SSDWorkerCount(), events, result),
			drainProgressCmd(events, result, m.p, func(f int) tea.Msg { return phase1DoneMsg{failures: f} }),
			progressTickCmd(),
		)

	case !r.SourceAvail && r.SSDAvail() && r.NASAvail():
		// Sync SSD→NAS only.
		missing := m.selectedIn(r.MissingOnNAS)
		if len(m.selected) > 0 && len(missing) == 0 {
			m.statusMsg = "No selected files need syncing to NAS."
			return m, nil
		}
		tasks, skipped := buildSyncTasks(missing, m.cfg, r)
		m.skippedNoRoot = skipped
		if len(tasks) == 0 {
			if skipped > 0 {
				m.statusMsg = fmt.Sprintf("%d file(s) skipped — NAS category root not mounted.", skipped)
			} else {
				m.statusMsg = "NAS is already up to date."
			}
			return m, nil
		}
		if err := copyop.CheckSpace(tasks); err != nil {
			m.statusMsg = err.Error()
			return m, nil
		}
		m.resetProgress(len(tasks), copyop.TotalSize(tasks))
		m.progressMode = modeSync
		m.screen = screenProgress
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelBatch = cancel
		events := make(chan copyop.FileProgress, 64)
		result := make(chan int, 1)
		return m, tea.Batch(
			runBatchCmd(ctx, tasks, m.logger, false, m.cfg.NASWorkerCount(), events, result),
			drainProgressCmd(events, result, m.p, func(f int) tea.Msg { return copyDoneMsg{failures: f} }),
			progressTickCmd(),
		)

	default:
		switch {
		case r.SourceAvail && !r.SSDAvail():
			m.statusMsg = "Camera found but no SSD root is mounted — cannot copy."
		case !r.SourceAvail && r.SSDAvail() && !r.NASAvail():
			m.statusMsg = "No camera, and NAS is not available — nothing to sync."
		default:
			m.statusMsg = "Nothing to do — no source device available."
		}
		return m, nil
	}
}

// startPhase2 launches the SSD→NAS copy phase after user confirms.
func (m *Model) startPhase2() (tea.Model, tea.Cmd) {
	if err := copyop.CheckSpace(m.phase2Tasks); err != nil {
		m.screen = screenDone
		m.doneMsg = err.Error()
		return m, nil
	}
	m.resetProgress(len(m.phase2Tasks), copyop.TotalSize(m.phase2Tasks))
	m.failures = 0
	m.failedFiles = nil
	m.progressMode = modePhase2
	m.screen = screenProgress
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelBatch = cancel
	events := make(chan copyop.FileProgress, 64)
	result := make(chan int, 1)
	return m, tea.Batch(
		runBatchCmd(ctx, m.phase2Tasks, m.logger, false, m.cfg.NASWorkerCount(), events, result),
		drainProgressCmd(events, result, m.p, func(f int) tea.Msg { return copyDoneMsg{failures: f} }),
		progressTickCmd(),
	)
}

// startVerify launches the verify pass with live per-file progress.
func (m *Model) startVerify() (tea.Model, tea.Cmd) {
	if m.status == nil || (!m.status.SourceAvail && !m.status.SSDAvail()) {
		m.statusMsg = "Nothing to verify — no camera or SSD available."
		return m, nil
	}
	m.verifyDone = 0
	m.verifyTotal = 0
	m.verifyIssues = nil
	m.failures = 0
	m.failedFiles = nil
	m.progressMode = modeVerify
	m.screen = screenProgress
	return m, verifyCmd(m.cfg, m.logger, m.p)
}

// gridThumbH is the height in text rows of one thumbnail in the grid.
const gridThumbH = 8

// gridCols computes the number of thumbnail columns that fit inside the
// grid screen's border.
func (m *Model) gridCols() int {
	thumbW := 16 // approximate cell width per thumbnail
	cols := (m.width - 2) / thumbW
	if cols < 1 {
		cols = 1
	}
	return cols
}

// currentFile returns the currently focused FileInfo (nil if not on a file node).
func (m *Model) currentFile() *scan.FileInfo {
	if len(m.visible) == 0 || m.cursor >= len(m.visible) {
		return nil
	}
	node := m.visible[m.cursor]
	if node.level != 3 {
		return nil
	}
	f := m.tree[node.year][node.month][node.day][node.fileIdx]
	return &f
}

// View renders the current screen.
func (m *Model) View() string {
	switch m.screen {
	case screenLoading:
		return m.renderLoading()
	case screenMain:
		return m.renderMain()
	case screenGrid:
		return m.renderGrid()
	case screenPreview:
		return m.renderPreview()
	case screenProgress:
		return m.renderProgress()
	case screenConfirm:
		return m.renderConfirm()
	case screenDone:
		return m.renderDone()
	case screenErrors:
		return m.renderErrors()
	case screenHelp:
		return m.renderHelp()
	}
	return ""
}

// ── helpers ───────────────────────────────────────────────────────────────────

func fmtBytes(n int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1f GB", float64(n)/GB)
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/MB)
	case n >= KB:
		return fmt.Sprintf("%.1f KB", float64(n)/KB)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// fmtDuration renders a duration compactly: "45s", "3m04s", "1h02m".
func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	min := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, min)
	case min > 0:
		return fmt.Sprintf("%dm%02ds", min, sec)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

func progressBar(width int, done, total int) string {
	if total == 0 || width <= 0 {
		return strings.Repeat("░", width)
	}
	filled := done * width / total
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return styleProgressBar.Render(bar)
}

// relPathSet builds a lowercase relpath lookup set from scanned files.
func relPathSet(files []scan.FileInfo) map[string]bool {
	set := make(map[string]bool, len(files))
	for _, f := range files {
		set[strings.ToLower(f.RelPath)] = true
	}
	return set
}

// skippedNote formats the "N skipped" suffix for done messages, or "" when
// nothing was skipped.
func skippedNote(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" %d file(s) skipped — category root not mounted.", n)
}

func truncate(s string, w int) string {
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	return "…" + string(runes[len(runes)-w+1:])
}

// renderLoading renders the loading screen.
func (m *Model) renderLoading() string {
	body := lipgloss.Place(m.width-2, m.height-3, lipgloss.Center, lipgloss.Center,
		styleTitle.Render("camera-backup")+"\n\n"+m.statusMsg,
	)
	return m.screenFrame("camera-backup", body, "[q] quit")
}

// renderConfirm renders the Phase 1 done / confirm Phase 2 screen.
func (m *Model) renderConfirm() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n  Copied %d of %d files to SSD.\n", m.copyTotal-m.failures, m.copyTotal))
	if m.failures > 0 {
		sb.WriteString(styleErr.Render(fmt.Sprintf("  %d files failed.", m.failures)) + "\n")
	}
	sb.WriteString(fmt.Sprintf("\n  %d files to copy SSD → NAS.\n", len(m.phase2Tasks)))
	if m.skippedNoRoot > 0 {
		sb.WriteString(styleWarn.Render(fmt.Sprintf("  %d file(s) skipped — NAS category root not mounted.", m.skippedNoRoot)) + "\n")
	}
	sb.WriteString("\n  " + styleOK.Render("[y]") + " Start Phase 2   " + styleDim.Render("[n] Skip"))
	return m.screenFrame("Phase 1 Complete", sb.String(), "[y] start Phase 2  [n] skip")
}
