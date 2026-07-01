package tui

import (
	"fmt"
	"image"
	"log"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/copyop"
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
)

type progressMode int

const (
	modePhase1  progressMode = iota // Camera→SSD verify
	modePhase2                      // SSD→NAS fast
	modeSync                        // SSD→NAS sync (no camera)
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

	// tabs: e.g. ["All (312)", "Missing on SSD (12)", "Missing on NAS (47)"]
	tabs      []string
	tabKeys   []tabKey // parallel to tabs; identifies what each tab shows
	activeTab int

	// tree
	allFiles     []scan.FileInfo // files shown in current tab
	tree         treeFiles
	yearOrder    []string                    // sorted years
	monthOrder   map[string][]string         // year → sorted months
	dayOrder     map[string]map[string][]string // year → month → sorted days
	expanded     map[string]bool             // "year", "year/month", "year/month/day" → expanded
	visible      []treeNode
	cursor       int

	// detail / preview
	thumbCache   map[string]image.Image // absPath → decoded image (nil = unsupported)
	loadingThumb string
	prevScreen   Screen
	gridYear     string
	gridMonth    string
	gridDay      string
	gridCursor   int

	// copy/verify progress
	progressMode progressMode
	fileProgress map[string]copyop.FileProgress
	copyDone     int
	copyTotal    int
	failedFiles  []copyop.FileProgress
	events       chan copyop.FileProgress

	// verify progress
	verifyResults []verify.FileResult
	verifyDone    int
	verifyTotal   int

	failures int
	doneMsg  string
	lastErr  error

	// phase2 tasks cached for the confirm screen
	phase2Tasks []copyop.Task
}

type tabKey int

const (
	tabAll           tabKey = iota
	tabMissingOnSSD
	tabMissingOnNAS
)

// New creates a new TUI model.
func New(cfg *config.Config, logger *log.Logger) *Model {
	return &Model{
		cfg:        cfg,
		logger:     logger,
		screen:     screenLoading,
		statusMsg:  "Scanning devices…",
		thumbCache: map[string]image.Image{},
		expanded:   map[string]bool{},
		fileProgress: map[string]copyop.FileProgress{},
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
		if msg.err != nil {
			m.lastErr = msg.err
			m.screen = screenDone
			m.doneMsg = "Error scanning devices: " + msg.err.Error()
			return m, nil
		}
		m.status = msg.result
		m.buildTabs()
		m.setTab(m.activeTab)
		m.screen = screenMain
		m.statusMsg = ""
		return m, nil

	case deviceChangedMsg:
		m.statusMsg = "Rescanning…"
		return m, statusScanCmd(m.cfg, m.logger)

	case fileProgressMsg:
		fp := msg.p
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
		if m.status != nil && m.status.NASAvail && len(m.phase2Tasks) > 0 {
			m.screen = screenConfirm
			return m, nil
		}
		m.screen = screenDone
		m.doneMsg = fmt.Sprintf("Phase 1 complete. %d files copied.", m.copyTotal)
		return m, statusScanCmd(m.cfg, m.logger)

	case copyDoneMsg:
		m.failures += msg.failures
		m.screen = screenDone
		m.doneMsg = "Copy complete."
		return m, statusScanCmd(m.cfg, m.logger)

	case verifyDoneMsg:
		m.verifyDone = msg.total
		m.verifyTotal = msg.total
		m.screen = screenDone
		if msg.bad == 0 {
			m.doneMsg = fmt.Sprintf("All %d files verified OK.", msg.total)
		} else {
			m.doneMsg = fmt.Sprintf("%d / %d files have issues.", msg.bad, msg.total)
		}

	case thumbnailMsg:
		if msg.err == nil && msg.img != nil {
			m.thumbCache[msg.file] = msg.img
		} else if msg.err == nil {
			m.thumbCache[msg.file] = nil // unsupported type; don't retry
		}
		if m.loadingThumb == msg.file {
			m.loadingThumb = ""
		}
	}

	return m, nil
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
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
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
		case "r":
			m.screen = screenLoading
			m.statusMsg = "Scanning devices…"
			m.fileProgress = map[string]copyop.FileProgress{}
			m.failures = 0
			m.failedFiles = nil
			m.copyDone, m.copyTotal = 0, 0
			return m, statusScanCmd(m.cfg, m.logger)
		case "e":
			if len(m.failedFiles) > 0 {
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
			// File node: open full preview.
			m.prevScreen = screenMain
			m.screen = screenPreview
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
			m.screen = screenGrid
		}

	case "y", "Y":
		return m.startCopy()

	case "a":
		// select/deselect all (future selection feature placeholder)

	case " ":
		// toggle selection (future feature placeholder)
	}
	return m, nil
}

func (m *Model) handleGridKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	switch msg.String() {
	case "esc", "q":
		m.screen = m.prevScreen
	case "ctrl+c":
		return m, tea.Quit
	case "p", "enter":
		if m.gridCursor < len(files) {
			m.screen = screenPreview
		}
	case "left", "h":
		if m.gridCursor > 0 {
			m.gridCursor--
		}
	case "right", "l":
		if m.gridCursor < len(files)-1 {
			m.gridCursor++
		}
	case "up", "k":
		cols := m.gridCols()
		if m.gridCursor >= cols {
			m.gridCursor -= cols
		}
	case "down", "j":
		cols := m.gridCols()
		if m.gridCursor+cols < len(files) {
			m.gridCursor += cols
		}
	}
	return m, nil
}

func (m *Model) handlePreviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	switch msg.String() {
	case "esc", "q":
		if m.prevScreen == screenGrid {
			m.screen = screenGrid
		} else {
			m.screen = screenMain
		}
	case "ctrl+c":
		return m, tea.Quit
	case "left", "h":
		if m.gridCursor > 0 {
			m.gridCursor--
		}
	case "right", "l":
		if m.gridCursor < len(files)-1 {
			m.gridCursor++
		}
	}
	return m, nil
}

// buildTabs constructs the tab list based on device availability.
func (m *Model) buildTabs() {
	m.tabs = nil
	m.tabKeys = nil
	r := m.status

	if r.SourceAvail || r.SSDAvail {
		count := len(r.CameraFiles)
		if !r.SourceAvail {
			count = len(r.SSDFiles)
		}
		m.tabs = append(m.tabs, fmt.Sprintf("All (%d)", count))
		m.tabKeys = append(m.tabKeys, tabAll)
	}
	if r.SourceAvail && r.SSDAvail {
		m.tabs = append(m.tabs, fmt.Sprintf("Missing on SSD (%d)", len(r.MissingOnSSD)))
		m.tabKeys = append(m.tabKeys, tabMissingOnSSD)
	}
	if r.NASAvail {
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

// maybeLoadThumb fires thumbnailCmd for the focused file if not already cached/loading.
func (m *Model) maybeLoadThumb() tea.Cmd {
	if len(m.visible) == 0 || m.cursor >= len(m.visible) {
		return nil
	}
	node := m.visible[m.cursor]
	if node.level != 3 {
		return nil
	}
	f := m.tree[node.year][node.month][node.day][node.fileIdx]
	if _, cached := m.thumbCache[f.AbsPath]; cached {
		return nil
	}
	if m.loadingThumb == f.AbsPath {
		return nil
	}
	m.loadingThumb = f.AbsPath
	return thumbnailCmd(f.AbsPath)
}

// startCopy determines which phase(s) to run and launches them.
func (m *Model) startCopy() (tea.Model, tea.Cmd) {
	if m.status == nil {
		return m, nil
	}
	r := m.status

	m.failures = 0
	m.failedFiles = nil
	m.fileProgress = map[string]copyop.FileProgress{}

	switch {
	case r.SourceAvail && r.SSDAvail:
		// Phase 1: Camera→SSD (with verify); Phase 2: SSD→NAS (if available).
		tasks := buildPhase1Tasks(r, m.cfg)
		if len(tasks) == 0 && !r.NASAvail {
			return m, nil
		}
		m.copyTotal = len(tasks)
		m.copyDone = 0
		m.progressMode = modePhase1

		// Cache phase 2 tasks for after confirmation.
		if r.NASAvail {
			// Phase 2 tasks are built from the CURRENT status; after phase 1 completes
			// we re-scan — but for the confirm screen we pre-compute from missing-on-NAS.
			m.phase2Tasks = buildPhase2Tasks(r)
		}

		m.screen = screenProgress
		events := make(chan copyop.FileProgress, 64)
		m.events = events
		return m, tea.Batch(
			copyPhase1Cmd(tasks, m.cfg.SSD, m.logger, m.cfg.SSDWorkerCount(), events),
			drainProgressCmd(events, m.p),
		)

	case !r.SourceAvail && r.SSDAvail && r.NASAvail:
		// Sync SSD→NAS only.
		tasks := buildPhase2Tasks(r)
		m.copyTotal = len(tasks)
		m.copyDone = 0
		m.progressMode = modeSync
		m.screen = screenProgress
		events := make(chan copyop.FileProgress, 64)
		m.events = events
		return m, tea.Batch(
			syncCmd(tasks, m.cfg.NAS, m.logger, m.cfg.NASWorkerCount(), events),
			drainProgressCmd(events, m.p),
		)

	default:
		// Nothing to do.
		return m, nil
	}
}

// startPhase2 launches the SSD→NAS copy phase after user confirms.
func (m *Model) startPhase2() (tea.Model, tea.Cmd) {
	m.progressMode = modePhase2
	m.screen = screenProgress
	m.copyDone = 0
	m.copyTotal = len(m.phase2Tasks)
	m.fileProgress = map[string]copyop.FileProgress{}
	events := make(chan copyop.FileProgress, 64)
	m.events = events
	return m, tea.Batch(
		copyPhase2Cmd(m.phase2Tasks, m.cfg.NAS, m.logger, m.cfg.NASWorkerCount(), events),
		drainProgressCmd(events, m.p),
	)
}

// gridCols computes the number of thumbnail columns that fit in the current width.
func (m *Model) gridCols() int {
	thumbW := 16 // approximate cell width per thumbnail
	cols := m.width / thumbW
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

func truncate(s string, w int) string {
	if len(s) <= w {
		return s
	}
	return "…" + s[len(s)-w+1:]
}

func ssdNASStatus(f scan.FileInfo, r *status.StatusResult, cfg *config.Config) (onSSD, onNAS bool) {
	if r == nil {
		return false, false
	}
	cat := cfg.Category(f.RelPath)
	key := strings.ToLower(f.DestRelPath(cat))

	if r.SSDAvail {
		for _, sf := range r.SSDFiles {
			if strings.ToLower(sf.RelPath) == key {
				onSSD = true
				break
			}
		}
	}
	if r.NASAvail {
		for _, nf := range r.NASFiles {
			if strings.ToLower(nf.RelPath) == key {
				onNAS = true
				break
			}
		}
	}
	return
}

func freeBar(used, free int64) string {
	total := used + free
	if total == 0 {
		return "N/A"
	}
	pct := int(100 * used / total)
	return fmt.Sprintf("%d%%", pct)
}

func renderDevice(name string, avail bool, freeBytes int64) string {
	icon := styleDeviceOff.Render("❌")
	if avail {
		icon = styleDeviceOK.Render("✅")
	}
	label := styleDim.Render(name)
	if avail && freeBytes > 0 {
		label = fmt.Sprintf("%s %s free", name, fmtBytes(freeBytes))
	}
	return fmt.Sprintf("%s %s", icon, label)
}

// renderLoading renders the loading screen.
func (m *Model) renderLoading() string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		styleTitle.Render("camera-backup")+"\n\n"+m.statusMsg,
	)
}

// renderConfirm renders the Phase 1 done / confirm Phase 2 screen.
func (m *Model) renderConfirm() string {
	var sb strings.Builder
	sb.WriteString(styleTitle.Render("Phase 1 Complete") + "\n\n")
	sb.WriteString(fmt.Sprintf("  Copied %d files to SSD.\n", m.copyTotal))
	if m.failures > 0 {
		sb.WriteString(styleErr.Render(fmt.Sprintf("  %d files failed.\n", m.failures)))
	}
	sb.WriteString(fmt.Sprintf("\n  %d files to copy SSD → NAS.\n", len(m.phase2Tasks)))
	sb.WriteString("\n  " + styleOK.Render("[y]") + " Start Phase 2   " + styleDim.Render("[n] Skip"))
	return sb.String()
}
