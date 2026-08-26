package tui

import (
	"fmt"
	"image"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
)

// The Kitty image is placed from fileDetailLines, while the rows above it are
// produced by fileMetaLines. If they ever disagree the picture lands on top of
// the file's details.
func TestFileMetaLines_MatchesConstant(t *testing.T) {
	m := testModel(baseCfg(), "")
	f := scan.FileInfo{AbsPath: "/cam/DCIM/DSC_0001.NEF", RelPath: "DCIM/DSC_0001.NEF", Size: 1024}

	if got := len(m.fileMetaLines(&f, 40)); got != fileDetailLines {
		t.Errorf("fileMetaLines produced %d rows, want fileDetailLines = %d", got, fileDetailLines)
	}
}

// The preview rectangle has to stay inside the Info panel it is drawn over.
func TestInfoPreviewRect_StaysInsideThePanel(t *testing.T) {
	m := testModel(baseCfg(), "")
	m.width, m.height = 140, 40

	cols, rows, row, col, ok := m.infoPreviewRect()
	if !ok {
		t.Fatal("no preview rectangle on a 140×40 terminal")
	}
	detW := m.detailWidth()
	if left := m.width - detW + 1; col <= left {
		t.Errorf("col %d is on or left of the panel border at %d", col, left)
	}
	if right := col + cols - 1; right > m.width-1 {
		t.Errorf("image ends at column %d, past the panel's right border", right)
	}
	if row < 3 {
		t.Errorf("row %d overlaps the header or the panel border", row)
	}
	if bottom := row + rows - 1; bottom > m.height-2 {
		t.Errorf("image ends at row %d, past the panel's bottom border", bottom)
	}
}

func TestInfoPreviewRect_NoPanelOnNarrowTerminal(t *testing.T) {
	m := testModel(baseCfg(), "")
	m.width, m.height = 60, 30
	if _, _, _, _, ok := m.infoPreviewRect(); ok {
		t.Error("reported a preview rectangle although the Info panel is hidden")
	}
}

// kittyModel is a model on the main screen with a decoded thumbnail for the
// focused file, as if the terminal spoke the Kitty protocol.
func kittyModel(t *testing.T) *Model {
	t.Helper()
	m := navModel(t)
	m.kitty = true
	m.screen = screenMain
	for i, n := range m.visible {
		if n.level == 3 {
			m.cursor = i
			break
		}
	}
	f := m.currentFile()
	if f == nil {
		t.Fatal("no file under the cursor")
	}
	m.thumbCache[f.AbsPath] = image.NewRGBA(image.Rect(0, 0, 160, 120))
	return m
}

func TestSyncInfoPreview_DrawsOnceAndRedrawsOnMove(t *testing.T) {
	m := kittyModel(t)
	first := m.currentFile().AbsPath

	if cmd := m.syncInfoPreview(); cmd == nil {
		t.Fatal("no draw command for a file with a loaded thumbnail")
	}
	if !strings.Contains(m.kittyShown, first) {
		t.Fatalf("kittyShown = %q, want it to name %q", m.kittyShown, first)
	}
	// Nothing changed: a repaint must not re-send the image.
	if cmd := m.syncInfoPreview(); cmd != nil {
		t.Error("redrew an image that is already on screen")
	}

	// A resize moves the panel, so the image has to follow.
	m.width += 40
	m.kittyShown = ""
	if cmd := m.syncInfoPreview(); cmd == nil {
		t.Error("no redraw after the layout changed")
	}
}

// Moving onto a row with no picture — a date group, a video — takes the image
// away instead of leaving it hanging over unrelated text.
func TestSyncInfoPreview_ClearsWhenNothingToShow(t *testing.T) {
	m := kittyModel(t)
	m.syncInfoPreview()

	for i, n := range m.visible {
		if n.level == 2 {
			m.cursor = i
			break
		}
	}
	if cmd := m.syncInfoPreview(); cmd == nil {
		t.Fatal("no clear command after moving off the file")
	}
	if m.kittyShown != "" {
		t.Errorf("kittyShown = %q, want it forgotten", m.kittyShown)
	}
	// Already cleared: nothing more to do.
	if cmd := m.syncInfoPreview(); cmd != nil {
		t.Error("sent a second clear with no image on screen")
	}
}

func TestSyncInfoPreview_RespectsConfig(t *testing.T) {
	for _, mode := range []string{config.PreviewBlocks, config.PreviewOff} {
		m := kittyModel(t)
		m.cfg.ListPreviewMode = mode
		if cmd := m.syncInfoPreview(); cmd != nil {
			t.Errorf("list_preview = %q still drew a Kitty image", mode)
		}
		if m.kittyShown != "" {
			t.Errorf("list_preview = %q recorded a drawn image", mode)
		}
	}
}

// Without Kitty support the panel keeps its block art, whatever the config says.
func TestSyncInfoPreview_NoKittyTerminal(t *testing.T) {
	m := kittyModel(t)
	m.kitty = false
	if cmd := m.syncInfoPreview(); cmd != nil {
		t.Error("drew a Kitty image in a terminal that does not speak it")
	}
}

// Leaving the main screen takes the image with it — the grid and the settings
// form draw their own thing over those cells.
func TestKeyLeavingMainScreen_ClearsTheImage(t *testing.T) {
	m := kittyModel(t)
	m.syncInfoPreview()
	if m.kittyShown == "" {
		t.Fatal("nothing drawn to begin with")
	}

	m.Update(key("c")) // settings
	if m.screen != screenSettings {
		t.Fatalf("screen = %v, want the settings screen", m.screen)
	}
	if m.kittyShown != "" {
		t.Error("the image is still recorded as drawn after leaving the main screen")
	}
}

// list_preview = "kitty" is for a terminal the detection cannot see through:
// it turns the protocol on even where KittySupported said no.
func TestListPreviewKitty_ForcesTheProtocol(t *testing.T) {
	cfg := baseCfg()
	cfg.ListPreviewMode = config.PreviewKitty
	m := New(cfg, testLogger(), "")

	if !m.kitty || !m.kittyList() {
		t.Errorf("kitty = %v, kittyList = %v — want the protocol forced on", m.kitty, m.kittyList())
	}
}

// gridModel puts the model on the grid screen with thumbnails loaded for a
// day's files.
func gridModel(t *testing.T, files int) *Model {
	t.Helper()
	m := testModel(baseCfg(), "")
	m.kitty = true
	m.width, m.height = 120, 30
	day := time.Date(2026, 3, 25, 10, 0, 0, 0, time.Local)
	var infos []scan.FileInfo
	for i := 0; i < files; i++ {
		f := scan.FileInfo{
			AbsPath:     filepath.Join("/cam/DCIM", fmt.Sprintf("DSC_%04d.JPG", i)),
			RelPath:     fmt.Sprintf("DCIM/DSC_%04d.JPG", i),
			Size:        1024,
			CaptureTime: day,
		}
		infos = append(infos, f)
		m.thumbCache[f.AbsPath] = image.NewRGBA(image.Rect(0, 0, 160, 120))
	}
	m.allFiles = infos
	m.buildTree()
	m.rebuildVisible()
	m.gridYear, m.gridMonth, m.gridDay = "2026", "2026-03", "2026-03-25"
	m.screen = screenGrid
	return m
}

// Every thumbnail of the visible window gets a cell rectangle, and the
// rectangles tile the grid without overlapping or leaving the panel.
func TestGridPlacements_TileTheWindow(t *testing.T) {
	m := gridModel(t, 12)
	places := m.gridPlacements()
	if len(places) == 0 {
		t.Fatal("no placements for a grid full of loaded thumbnails")
	}

	cols := m.gridCols()
	if want := min(12, cols*m.gridVisibleRows()); len(places) != want {
		t.Fatalf("%d placements, want %d (the visible window)", len(places), want)
	}
	seen := map[[2]int]bool{}
	for _, p := range places {
		if p.row < 2 || p.col < 2 {
			t.Errorf("placement at %d,%d overlaps the panel border", p.row, p.col)
		}
		if right := p.col + p.cols - 1; right > m.width-1 {
			t.Errorf("placement ends at column %d, past the panel", right)
		}
		if bottom := p.row + p.rows - 1; bottom > m.height-2 {
			t.Errorf("placement ends at row %d, past the panel", bottom)
		}
		if seen[[2]int{p.row, p.col}] {
			t.Errorf("two thumbnails share the cell %d,%d", p.row, p.col)
		}
		seen[[2]int{p.row, p.col}] = true
	}
}

// Scrolling changes what is on screen, so the images are redrawn; scrolling
// back to an identical window does not re-send them.
func TestSyncGridPreview_RedrawsOnScroll(t *testing.T) {
	m := gridModel(t, 40)

	if cmd := m.syncGridPreview(); cmd == nil {
		t.Fatal("no draw command for the first screenful")
	}
	first := m.kittyShown
	if cmd := m.syncGridPreview(); cmd != nil {
		t.Error("redrew a window that has not changed")
	}

	m.gridOffset++
	if cmd := m.syncGridPreview(); cmd == nil {
		t.Fatal("no redraw after scrolling")
	}
	if m.kittyShown == first {
		t.Error("the signature did not change with the window")
	}
}

// Moving the cursor within the same window only changes the labels, which are
// text: the images must not be re-sent.
func TestSyncGridPreview_CursorMoveDoesNotRedraw(t *testing.T) {
	m := gridModel(t, 12)
	m.syncGridPreview()
	before := m.kittyShown

	m.gridCursor = 2
	if cmd := m.syncGridPreview(); cmd != nil {
		t.Error("moving the cursor re-sent the images")
	}
	if m.kittyShown != before {
		t.Error("the signature changed although the window did not")
	}
}

// Leaving the grid takes its images away.
func TestGrid_LeavingClearsTheImages(t *testing.T) {
	m := gridModel(t, 6)
	m.syncGridPreview()
	if m.kittyShown == "" {
		t.Fatal("nothing drawn to begin with")
	}

	m.Update(key("?")) // help screen: draws nothing of its own
	if m.screen != screenHelp {
		t.Fatalf("screen = %v, want the help screen", m.screen)
	}
	if m.kittyShown != "" {
		t.Error("the grid's images are still recorded as drawn")
	}
}

// With block art the grid draws its own cells and nothing goes on the graphics
// layer.
func TestSyncGridPreview_BlocksMode(t *testing.T) {
	m := gridModel(t, 6)
	m.cfg.ListPreviewMode = config.PreviewBlocks

	if cmd := m.syncGridPreview(); cmd != nil {
		t.Error("drew Kitty images although list_preview is blocks")
	}
	if len(m.gridPlacements()) == 0 {
		t.Error("gridPlacements is about geometry and should not depend on the mode")
	}
}
