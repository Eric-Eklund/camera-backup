package tui

import (
	"context"
	"errors"
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
	screenSettings        // editable config.toml
	screenDevices         // mounted devices, for picking the source
)

type progressMode int

const (
	modePhase1 progressMode = iota // Camera→SSD verify
	modePhase2                     // SSD→NAS fast
	modeSync                       // SSD→NAS sync (no camera)
	modeDirect                     // source→NAS verify, local SSD bypassed
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

	// configPath is where the settings screen writes; empty disables editing.
	configPath string
	settings   *settingsForm
	// picker is the device screen's state; nil when it is not open.
	picker *devicePicker
	// watcherStop stops the running device watcher. Closed and replaced when
	// the configured paths change, so the watcher follows the new ones.
	watcherStop chan struct{}

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
	thumbCache   map[string]image.Image // absPath → decoded image (nil = no preview)
	loadingThumb map[string]bool        // absPath → thumbnail load in flight
	fullCache    map[string]image.Image // absPath → full-size preview (nil = no preview)
	loadingFull  map[string]bool        // absPath → full image load in flight
	// rawToolMissing is set once a RAW preview fails because exiftool is not
	// installed, so the panels can say why instead of showing a blank box.
	rawToolMissing bool
	kitty          bool // terminal supports Kitty Graphics Protocol
	// kittyShown describes what the terminal is currently showing: which
	// files, at which cells. Redrawing only when that string changes keeps a
	// held-down j from clearing and re-sending images on every row.
	kittyShown string
	prevScreen Screen
	helpReturn Screen // screen to return to when help closes
	helpOffset int    // first visible help line; the reference outgrows short terminals
	gridYear   string
	gridMonth  string
	gridDay    string
	gridCursor int
	gridOffset int // first visible thumbnail row (scroll position)

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

// New creates a new TUI model. configPath is the config.toml the settings
// screen saves to; pass "" to make the settings screen read-only.
func New(cfg *config.Config, logger *log.Logger, configPath string) *Model {
	return &Model{
		cfg:          cfg,
		logger:       logger,
		configPath:   configPath,
		screen:       screenLoading,
		statusMsg:    "Scanning devices…",
		thumbCache:   map[string]image.Image{},
		loadingThumb: map[string]bool{},
		fullCache:    map[string]image.Image{},
		loadingFull:  map[string]bool{},
		// list_preview = "kitty" is the override for a terminal the detection
		// cannot see through — tmux with allow-passthrough, say.
		kitty:        preview.KittySupported() || cfg.ListPreview() == config.PreviewKitty,
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
		m.restartWatcher(),
	)
}

// restartWatcher stops any running device watcher and starts one for the
// currently configured paths. Called at startup and whenever the settings
// screen changes the paths, so a newly configured mount point is watched.
func (m *Model) restartWatcher() tea.Cmd {
	if m.watcherStop != nil {
		close(m.watcherStop)
	}
	m.watcherStop = make(chan struct{})
	return watchDevicesCmd(m.cfg, m.p, m.watcherStop)
}

// Update handles all messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Every panel moved, so anything drawn over one is in the wrong place.
		// The signature is left alone: it encodes the cells each image sits
		// in, so a real layout change forces a redraw by itself — and clearing
		// it first would tell kittyForget there is nothing to take away, which
		// is exactly the case when the terminal shrinks past the width that
		// carries an Info panel at all.
		return m, m.kittySync()

	case tea.KeyMsg:
		before := m.screen
		model, cmd := m.handleKey(msg)
		// Every screen change is a change to what belongs on the graphics
		// layer — a different set of images, or none. Catching it here covers
		// every key that moves between screens.
		if m.screen != before {
			cmd = tea.Batch(cmd, m.kittySync())
		}
		return model, cmd

	case statusReadyMsg: // scan finished: the tree is rebuilt below
		// Only apply on the loading/main/settings/devices screens — never yank
		// the user out of a running operation, confirm dialog, or preview. The
		// settings and device screens stay put: they trigger rescans themselves
		// and show the outcome in place.
		if m.screen != screenLoading && m.screen != screenMain &&
			m.screen != screenSettings && m.screen != screenDevices {
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
		if m.screen != screenSettings && m.screen != screenDevices {
			m.screen = screenMain
		}
		m.statusMsg = ""
		if msg.result.CameraUnstable > 0 {
			m.statusMsg = fmt.Sprintf("%d camera file(s) skipped — possibly still being written; rescan when the card is idle.", msg.result.CameraUnstable)
		}
		return m, m.maybeLoadThumb()

	case deviceChangedMsg:
		// Ignore device events during operations; a fresh scan runs when the
		// user returns to the main screen anyway. The settings screen accepts
		// them so plugging a device in updates its ✔/✘ markers live.
		if m.screen != screenMain && m.screen != screenLoading &&
			m.screen != screenSettings && m.screen != screenDevices {
			return m, nil
		}
		m.statusMsg = "Rescanning…"
		if m.screen == screenDevices && m.picker != nil {
			// A card just went in or came out: the list on screen is stale.
			m.picker.loading = true
			return m, tea.Batch(scanDevicesCmd(), statusScanCmd(m.cfg, m.logger))
		}
		return m, statusScanCmd(m.cfg, m.logger)

	case devicesReadyMsg:
		if m.picker == nil {
			return m, nil
		}
		m.picker.apply(msg.devs, msg.err)
		return m, nil

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
		// A direct dump leaves the NAS as the only copy, so an incomplete run
		// has to say plainly that the card is not safe to format yet.
		cardWarning := ""
		if m.progressMode == modeDirect {
			cardWarning = " Do not format the card yet."
		}
		switch {
		case m.cancelling:
			m.doneMsg = fmt.Sprintf("Cancelled: %d of %d files copied (%d skipped).%s",
				m.copyDone-m.failures, m.copyTotal, m.copyTotal-m.copyDone, cardWarning)
		case m.failures > 0:
			m.doneMsg = fmt.Sprintf("Copy finished: %d of %d files copied, %d failed.%s%s",
				m.copyDone-m.failures, m.copyTotal, m.failures, skippedNote(m.skippedNoRoot), cardWarning)
		case m.progressMode == modeDirect:
			m.doneMsg = fmt.Sprintf("Direct dump complete: %d file(s) copied and verified on the NAS.%s",
				m.copyDone, skippedNote(m.skippedNoRoot))
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
		case msg.bad == 0 && len(msg.skipped) == 0:
			m.doneMsg = fmt.Sprintf("All %d files verified OK.", msg.total)
		case msg.bad == 0:
			m.doneMsg = fmt.Sprintf("All %d files verified OK against what was checked.\nNot checked: %s",
				msg.total, strings.Join(msg.skipped, ", "))
		default:
			m.doneMsg = fmt.Sprintf("%d / %d files have issues.", msg.bad, msg.total)
			if len(msg.skipped) > 0 {
				m.doneMsg += "\nNot checked: " + strings.Join(msg.skipped, ", ")
			}
		}

	case thumbnailMsg:
		// Cache the failure as well as the success: without an entry the view
		// would ask for this thumbnail again on every render.
		m.thumbCache[msg.file] = msg.img
		if errors.Is(msg.err, preview.ErrNoRAWTool) {
			m.rawToolMissing = true
		}
		delete(m.loadingThumb, msg.file)
		// This is the only message that fills thumbCache, so it is where the
		// Info panel and the grid learn they have something to draw. Without
		// it a thumbnail that arrives while the cursor sits still is never
		// placed, and the reserved rows stay blank until the next keypress.
		switch m.screen {
		case screenMain:
			if f := m.currentFile(); f != nil && f.AbsPath == msg.file {
				return m, m.syncInfoPreview()
			}
		case screenGrid:
			return m, m.syncGridPreview()
		}

	case fullImageMsg:
		m.fullCache[msg.file] = msg.img // nil = no preview available
		if errors.Is(msg.err, preview.ErrNoRAWTool) {
			m.rawToolMissing = true
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

// kittyList reports whether the Info panel's preview should be drawn as a real
// image rather than block art.
func (m *Model) kittyList() bool {
	switch m.cfg.ListPreview() {
	case config.PreviewAuto, config.PreviewKitty:
		return m.kitty
	}
	return false
}

// kittySync draws what the current screen wants on the graphics layer, or
// takes away what is left over from the one before it.
func (m *Model) kittySync() tea.Cmd {
	switch m.screen {
	case screenMain:
		return m.syncInfoPreview()
	case screenGrid:
		return m.syncGridPreview()
	}
	return m.kittyForget()
}

// kittyForget clears the graphics layer if anything is on it.
func (m *Model) kittyForget() tea.Cmd {
	if m.kittyShown == "" {
		return nil
	}
	m.kittyShown = ""
	return kittyClearCmd()
}

// syncInfoPreview keeps the image over the Info panel in step with the cursor:
// it draws the focused file's thumbnail, moves it when the layout changes, and
// takes it away when the cursor lands on something that has no picture — a
// date group, a video, a RAW file with no reader for it.
func (m *Model) syncInfoPreview() tea.Cmd {
	if !m.kittyList() || m.screen != screenMain {
		return nil
	}

	var img image.Image
	path := ""
	if f := m.currentFile(); f != nil {
		if cached, ok := m.thumbCache[f.AbsPath]; ok && cached != nil {
			img, path = cached, f.AbsPath
		}
	}
	cols, rows, row, col, ok := m.infoPreviewRect()
	if path == "" || !ok {
		return m.kittyForget()
	}

	sig := fmt.Sprintf("info|%s|%d,%d,%d,%d", path, cols, rows, row, col)
	if m.kittyShown == sig {
		return nil // already on screen, in the right place
	}
	m.kittyShown = sig
	return kittyDrawCmd(img, cols, rows, row, col)
}

// syncGridPreview draws the thumbnails of the grid's visible window as real
// images. The grid is the screen that exists for looking at photographs, so a
// pixel per terminal column is felt there more than anywhere else.
func (m *Model) syncGridPreview() tea.Cmd {
	if !m.kittyList() || m.screen != screenGrid {
		return nil
	}
	places := m.gridPlacements()
	if len(places) == 0 {
		return m.kittyForget()
	}

	var sig strings.Builder
	sig.WriteString("grid")
	for _, p := range places {
		fmt.Fprintf(&sig, "|%s@%d,%d,%d,%d", p.path, p.cols, p.rows, p.row, p.col)
	}
	if m.kittyShown == sig.String() {
		return nil
	}
	m.kittyShown = sig.String()
	return kittyDrawGridCmd(places)
}

// gridPlacements lists where each loaded thumbnail of the visible window goes,
// in 1-indexed terminal cells. renderGrid leaves exactly these cells blank, so
// the two have to agree on which files are drawn as images.
func (m *Model) gridPlacements() []kittyPlacement {
	files := m.dayFiles(m.gridYear, m.gridMonth, m.gridDay)
	cols := m.gridCols()
	if cols < 1 || len(files) == 0 {
		return nil
	}
	cellW := (m.width - 2) / cols
	if cellW < 6 {
		return nil
	}
	start := m.gridOffset * cols
	end := (m.gridOffset + m.gridVisibleRows()) * cols
	if end > len(files) {
		end = len(files)
	}

	var out []kittyPlacement
	for i := start; i < end && i >= 0; i++ {
		img, ok := m.thumbCache[files[i].AbsPath]
		if !ok || img == nil {
			continue // still loading, or nothing to show: the box stays
		}
		rowIdx, colIdx := (i-start)/cols, (i-start)%cols
		out = append(out, kittyPlacement{
			img:  img,
			path: files[i].AbsPath,
			cols: cellW - 2,
			rows: gridThumbH,
			// Row 1 is the panel's top border; each grid row is a thumbnail
			// plus its label. Column 1 is the border as well.
			row: 2 + rowIdx*(gridThumbH+1),
			col: 2 + colIdx*cellW,
		})
	}
	return out
}

// infoPreviewRect is where the Info panel's preview sits, in 1-indexed
// terminal cells: the panel starts after the tree, and the picture starts
// under the file's details. ok is false when the panel is too small to hold
// one — or absent entirely on a narrow terminal.
func (m *Model) infoPreviewRect() (cols, rows, row, col int, ok bool) {
	detW := m.detailWidth()
	if detW == 0 {
		return 0, 0, 0, 0, false
	}
	midH := m.height - 2
	if midH < 4 {
		midH = 4
	}
	// renderDetailPanel is handed detW-2 columns and midH-2 rows; the art is
	// inset a further 2 columns and leaves a row at the bottom.
	cols = detW - 4
	rows = midH - 2 - fileDetailLines - 1
	// Row 1 is the header, row 2 the panel's top border, so its first inner
	// row is 3 — and the details take fileDetailLines of them.
	row = 3 + fileDetailLines
	col = m.width - detW + 2
	return cols, rows, row, col, cols > 4 && rows > 4
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

	case screenDevices:
		return m.handleDevicesKey(msg)

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
		case "j", "down":
			m.scrollHelp(1)
		case "k", "up":
			m.scrollHelp(-1)
		case "ctrl+c":
			return m, tea.Quit
		}

	case screenSettings:
		return m.handleSettingsKey(msg)
	}
	return m, nil
}

// handleSettingsKey drives the settings screen. While a field is being typed
// into, almost every key belongs to the editor — only enter and esc get out.
func (m *Model) handleSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.settings
	if f == nil {
		m.screen = screenMain
		return m, nil
	}

	if f.editing {
		switch msg.String() {
		case "enter":
			f.commitEdit()
		case "esc":
			f.cancelEdit()
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+u":
			f.editor.clear()
		case "backspace":
			f.editor.backspace()
		case "delete":
			f.editor.deleteForward()
		case "left":
			f.editor.left()
		case "right":
			f.editor.right()
		case "home", "ctrl+a":
			f.editor.home()
		case "end", "ctrl+e":
			f.editor.end()
		default:
			// Printable input only — ignore the rest so a stray control key
			// cannot corrupt a path.
			switch msg.Type {
			case tea.KeyRunes:
				f.editor.insert(msg.Runes)
			case tea.KeySpace:
				f.editor.insert([]rune{' '})
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "j", "down":
		f.moveCursor(1)

	case "k", "up":
		f.moveCursor(-1)

	case "enter", " ":
		f.startEdit()

	case "d":
		if !f.canPickDevice() {
			f.err = "the device list fills the source fields — move to one of those"
			return m, nil
		}
		f.err, f.notice = "", ""
		m.picker = newDevicePicker(pickerField, m.cfg.ActiveSource(), f.fields[f.cursor].label)
		m.screen = screenDevices
		return m, scanDevicesCmd()

	case "s":
		return m, m.saveSettings()

	case "r":
		// Reload from disk, discarding edits.
		cfg, err := config.Load(f.configPath)
		if err != nil {
			f.err = err.Error()
			return m, nil
		}
		m.settings = newSettingsForm(cfg, f.configPath)
		m.settings.notice = "Reloaded from " + f.configPath
		return m, nil

	case "esc", "q":
		if f.dirty && !f.confirmExit {
			f.confirmExit = true
			f.err = ""
			f.notice = ""
			return m, nil
		}
		m.screen = screenMain
		m.settings = nil
	}
	return m, nil
}

// saveSettings validates the form, writes config.toml, and adopts the result:
// the device watcher is restarted on the new paths and a fresh scan is kicked
// off, so edited paths take effect without restarting the TUI.
func (m *Model) saveSettings() tea.Cmd {
	f := m.settings
	if f.configPath == "" {
		f.err = "no config file path — cannot save"
		return nil
	}
	draft, err := f.toConfig(m.cfg)
	if err != nil {
		f.err = err.Error()
		f.notice = ""
		return nil
	}
	if err := draft.Save(f.configPath); err != nil {
		f.err = err.Error()
		f.notice = ""
		return nil
	}

	// config.toml now spells out the source devices, so a device picked
	// earlier in this session must stop overriding what was just saved.
	draft.SetSourceOverride("")
	m.cfg = draft
	f.dirty = false
	f.confirmExit = false
	f.err = ""
	f.notice = "Saved to " + f.configPath + " — rescanning devices…"
	m.logger.Printf("config saved to %s", f.configPath)

	// Selections point at files from the previous scan, which a path change can
	// make meaningless — start clean.
	m.selected = map[string]bool{}
	m.statusMsg = "Rescanning after config change…"
	return tea.Batch(m.restartWatcher(), statusScanCmd(m.cfg, m.logger))
}

// openDevicePicker shows the mounted devices so the source can be swapped
// without editing config.toml.
func (m *Model) openDevicePicker() (tea.Model, tea.Cmd) {
	// The last scan's source is what the file list on screen describes; before
	// the first scan lands, fall back to what the config resolves to.
	active := m.cfg.ActiveSource()
	if m.status != nil {
		active = m.status.Source
	}
	m.picker = newDevicePicker(pickerSwap, active, "")
	m.screen = screenDevices
	return m, scanDevicesCmd()
}

func (m *Model) handleDevicesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.picker
	if p == nil {
		m.screen = screenMain
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "j", "down":
		p.moveCursor(1)

	case "k", "up":
		p.moveCursor(-1)

	case "r":
		p.loading, p.err, p.notice = true, "", ""
		return m, scanDevicesCmd()

	case "enter", " ":
		d, ok := p.current()
		if !ok {
			break
		}
		if p.mode == pickerField {
			return m, m.fillSettingsPath(d.Path)
		}
		return m, m.useDevice(d)

	case "s":
		// Only the swap picker writes config.toml directly; in field mode the
		// settings screen's own [s] does the saving.
		if p.mode != pickerSwap {
			break
		}
		d, ok := p.current()
		if !ok {
			break
		}
		return m, m.saveDeviceAsSource(d)

	case "esc", "q":
		m.closePicker()
	}
	return m, nil
}

// closePicker returns to whichever screen opened the picker.
func (m *Model) closePicker() {
	if m.picker != nil && m.picker.mode == pickerField && m.settings != nil {
		m.screen = screenSettings
	} else {
		m.screen = screenMain
	}
	m.picker = nil
}

// fillSettingsPath writes a picked device path into the focused settings field
// and returns to the form, where it is saved like any typed value.
func (m *Model) fillSettingsPath(path string) tea.Cmd {
	if m.settings != nil {
		m.settings.applyPickedPath(path)
	}
	m.picker = nil
	m.screen = screenSettings
	return nil
}

func (m *Model) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		if m.kittyShown != "" {
			preview.KittyClear()
		}
		return m, tea.Quit

	case "tab":
		if len(m.tabs) > 0 {
			m.setTab((m.activeTab + 1) % len(m.tabs))
		}

	case "shift+tab":
		if len(m.tabs) > 0 {
			m.setTab((m.activeTab - 1 + len(m.tabs)) % len(m.tabs))
		}

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

	case "l", "right":
		return m.enterNode()

	case "h", "left":
		return m.leaveNode()

	case "]", "}":
		return m.jumpDay(1)

	case "[", "{":
		return m.jumpDay(-1)

	case "z":
		return m.setAllExpanded(false)

	case "Z":
		return m.setAllExpanded(true)

	case "f", "F":
		return m.focusCurrent()

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

	case "d", "D":
		return m.openDevicePicker()

	case "c", "C":
		m.settings = newSettingsForm(m.cfg, m.configPath)
		if m.configPath == "" {
			m.settings.err = "started without a config file — settings are read-only"
		}
		m.screen = screenSettings

	case "?":
		m.helpReturn = screenMain
		m.helpOffset = 0
		m.screen = screenHelp
	}
	return m, nil
}

// enterNode is `l`/`right`: step into what the cursor is on. A collapsed group
// opens, an open one hands the cursor to its first child, and a file opens its
// preview — the same motion all the way down the tree.
func (m *Model) enterNode() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 {
		return m, nil
	}
	node := m.visible[m.cursor]
	if node.level == 3 {
		m.prevScreen = screenMain
		m.gridYear, m.gridMonth, m.gridDay = node.year, node.month, node.day
		m.gridCursor = node.fileIdx
		m.screen = screenPreview
		return m, m.loadThumbsForPreview()
	}

	key := nodeKey(node)
	if !m.expanded[key] {
		m.expanded[key] = true
		m.rebuildVisible()
		return m, nil
	}
	// Already open: the first child is the row right below it, unless the
	// group turned out to be empty.
	if m.cursor+1 < len(m.visible) && m.visible[m.cursor+1].level > node.level {
		m.cursor++
		return m, m.maybeLoadThumb()
	}
	return m, nil
}

// leaveNode is `h`/`left`: step back out. An open group closes where it
// stands; anything else — a closed group, or a file — hands the cursor to its
// parent and closes that, so `h` from deep in a date walks back up one level
// per press instead of stranding the cursor inside a folder it just left.
func (m *Model) leaveNode() (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 {
		return m, nil
	}
	node := m.visible[m.cursor]
	if node.level < 3 && m.expanded[nodeKey(node)] {
		m.expanded[nodeKey(node)] = false
		m.rebuildVisible()
		return m, nil
	}

	parent := m.parentIndex(m.cursor)
	if parent < 0 {
		return m, nil // a closed year: there is nothing above it
	}
	m.expanded[nodeKey(m.visible[parent])] = false
	m.cursor = parent
	m.rebuildVisible()
	return m, m.maybeLoadThumb()
}

// jumpDay moves to the next (dir=1) or previous (dir=-1) date row. A day can
// hold hundreds of frames, so stepping over one with j/k is not navigation —
// and going back lands on the day the cursor is already inside, which is the
// quickest way to the top of a long day.
func (m *Model) jumpDay(dir int) (tea.Model, tea.Cmd) {
	for i := m.cursor + dir; i >= 0 && i < len(m.visible); i += dir {
		if m.visible[i].level == 2 {
			m.cursor = i
			return m, m.maybeLoadThumb()
		}
	}
	return m, nil
}

// setAllExpanded folds the whole tree open or shut. A scan of a full card
// opens every day at once, which is the wrong altitude to start from: z gives
// back the list of years, Z puts every frame back on screen.
func (m *Model) setAllExpanded(open bool) (tea.Model, tea.Cmd) {
	var focus treeNode
	if m.cursor < len(m.visible) {
		focus = m.visible[m.cursor]
	}
	for key := range m.expanded {
		m.expanded[key] = open
	}
	m.rebuildVisible()
	m.relocate(focus)
	return m, m.maybeLoadThumb()
}

// focusCurrent closes every group except the one the cursor is in, leaving the
// day being worked on open in an otherwise folded tree.
func (m *Model) focusCurrent() (tea.Model, tea.Cmd) {
	if m.cursor >= len(m.visible) {
		return m, nil
	}
	focus := m.visible[m.cursor]
	for key := range m.expanded {
		m.expanded[key] = false
	}
	m.expanded[focus.year] = true
	if focus.level >= 1 {
		m.expanded[focus.year+"/"+focus.month] = true
	}
	if focus.level >= 2 {
		m.expanded[focus.year+"/"+focus.month+"/"+focus.day] = true
	}
	m.rebuildVisible()
	m.relocate(focus)
	return m, m.maybeLoadThumb()
}

// relocate puts the cursor back on target after a rebuild. When target itself
// is no longer listed — its group was just folded shut — the cursor settles on
// the nearest ancestor that is, so a fold never dumps the user at row 0.
func (m *Model) relocate(target treeNode) {
	for level := target.level; level >= 0; level-- {
		probe := target
		probe.level = level
		if i := m.indexOf(probe); i >= 0 {
			m.cursor = i
			return
		}
	}
}

// indexOf finds the row showing n, or -1 when it is not currently listed.
func (m *Model) indexOf(n treeNode) int {
	for i, v := range m.visible {
		if v.level != n.level || v.year != n.year {
			continue
		}
		if n.level >= 1 && v.month != n.month {
			continue
		}
		if n.level >= 2 && v.day != n.day {
			continue
		}
		if n.level == 3 && v.fileIdx != n.fileIdx {
			continue
		}
		return i
	}
	return -1
}

// parentIndex returns the row holding the group that contains idx, or -1 for a
// top-level row. The visible list is a flattened depth-first walk, so the
// parent is the nearest row above with one level less.
func (m *Model) parentIndex(idx int) int {
	if idx <= 0 || idx >= len(m.visible) {
		return -1
	}
	level := m.visible[idx].level
	for i := idx - 1; i >= 0; i-- {
		if m.visible[i].level == level-1 {
			return i
		}
	}
	return -1
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
		m.helpOffset = 0
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
	// Every scroll and every entry into the grid comes through here, which
	// makes it the place to keep the drawn images in step with the window.
	return tea.Batch(append(cmds, m.syncGridPreview())...)
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

	// With no source device the SSD tree is browsable instead — but not in
	// direct mode, where the SSD is not part of the picture at all.
	if r.SourceAvail || (r.SSDAvail() && m.cfg.SSDInUse()) {
		count := len(r.CameraFiles)
		if !r.SourceAvail {
			count = len(r.SSDFiles)
		}
		m.tabs = append(m.tabs, fmt.Sprintf("All (%d)", count))
		m.tabKeys = append(m.tabKeys, tabAll)
	}
	// In direct mode the SSD takes no part in the run, so there is nothing
	// meaningful to be "missing" from it.
	if r.SourceAvail && r.SSDAvail() && m.cfg.SSDInUse() {
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
		// Group by DateTaken so the tree matches the destination layout that
		// DestRelPath produces, not the date the card happened to be written.
		taken := f.DateTaken()
		year := taken.Format("2006")
		month := taken.Format("2006-01")
		day := taken.Format("2006-01-02")

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
// maybeLoadThumb loads what the focused row needs a picture of, and — since it
// runs on every cursor move — is also where the Info panel's image is brought
// in step with the cursor.
func (m *Model) maybeLoadThumb() tea.Cmd {
	if len(m.visible) == 0 || m.cursor >= len(m.visible) {
		return m.syncInfoPreview()
	}
	node := m.visible[m.cursor]
	switch node.level {
	case 3:
		f := m.tree[node.year][node.month][node.day][node.fileIdx]
		return tea.Batch(m.loadThumb(f.AbsPath), m.syncInfoPreview())
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
		return tea.Batch(append(cmds, m.syncInfoPreview())...)
	}
	return m.syncInfoPreview()
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

// launchBatch starts a copy batch: it resets per-batch progress state, switches
// to the progress screen, and wires the worker pool to the progress drain.
// mkDone builds the message emitted once every progress event has been
// forwarded (see drainProgressCmd).
func (m *Model) launchBatch(mode progressMode, tasks []copyop.Task, doVerify bool, writeTimeout time.Duration, workers int, mkDone func(failures int) tea.Msg) tea.Cmd {
	m.resetProgress(len(tasks), copyop.TotalSize(tasks))
	m.progressMode = mode
	m.screen = screenProgress
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelBatch = cancel
	events := make(chan copyop.FileProgress, 64)
	result := make(chan int, 1)
	return tea.Batch(
		runBatchCmd(ctx, tasks, m.logger, doVerify, writeTimeout, workers, events, result),
		drainProgressCmd(events, result, m.p, mkDone),
		progressTickCmd(),
	)
}

// startCopy determines which phase(s) to run and launches them.
// If a selection is active, only selected files are copied (camera→SSD, direct
// and sync modes); Phase 2 after a camera copy always pushes everything missing.
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
	case m.cfg.DirectToNAS && r.SourceAvail && r.NASAvail():
		// Direct dump: source→NAS with verification, local SSD bypassed.
		missing := m.selectedIn(r.MissingOnNAS)
		if len(m.selected) > 0 && len(missing) == 0 {
			m.statusMsg = "No selected files need copying to the NAS."
			return m, nil
		}
		tasks, skipped := buildDirectTasks(missing, m.cfg, r)
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
		return m, m.launchBatch(modeDirect, tasks, true, m.cfg.NASWriteTimeout(), m.cfg.NASWorkerCount(),
			func(f int) tea.Msg { return copyDoneMsg{failures: f} })

	case m.cfg.DirectToNAS && r.SourceAvail:
		m.statusMsg = "Direct mode: NAS not available — mount the share (or connect the VPN) and rescan."
		return m, nil

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
		return m, m.launchBatch(modePhase1, tasks, true, 0, m.cfg.SSDWorkerCount(),
			func(f int) tea.Msg { return phase1DoneMsg{failures: f} })

	case !r.SourceAvail && r.SSDAvail() && r.NASAvail() && m.cfg.SSDInUse():
		// Sync SSD→NAS only. Never in direct mode: the SSD is hidden from the
		// UI there, so copying from it behind the user's back would be a
		// surprise — `camera-backup sync` is the explicit way to ask for it.
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
		return m, m.launchBatch(modeSync, tasks, false, m.cfg.NASWriteTimeout(), m.cfg.NASWorkerCount(),
			func(f int) tea.Msg { return copyDoneMsg{failures: f} })

	default:
		switch {
		case m.cfg.DirectToNAS && !r.SourceAvail:
			m.statusMsg = "Direct mode: no source device mounted — insert a card or connect a drive."
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
	m.failures = 0
	m.failedFiles = nil
	return m, m.launchBatch(modePhase2, m.phase2Tasks, false, m.cfg.NASWriteTimeout(), m.cfg.NASWorkerCount(),
		func(f int) tea.Msg { return copyDoneMsg{failures: f} })
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
// scrollHelp moves the help window, stopping at the last line that has
// anything below it so the screen never scrolls past its own content.
func (m *Model) scrollHelp(delta int) {
	avail := m.helpHeight()
	total := len(helpBody(m.helpBlocks(), avail))
	m.helpOffset += delta
	if max := total - avail; m.helpOffset > max {
		m.helpOffset = max
	}
	if m.helpOffset < 0 {
		m.helpOffset = 0
	}
}

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
	case screenSettings:
		return m.renderSettings()
	case screenDevices:
		return m.renderDevices()
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
