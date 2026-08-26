package tui

import (
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Eric-Eklund/camera-backup/internal/scan"
)

// navModel builds a model whose tree holds two days in one month, so every
// level of the walk (year → month → day → file) is exercised.
func navModel(t *testing.T) *Model {
	t.Helper()
	m := testModel(baseCfg(), "")
	m.width, m.height = 120, 40
	file := func(name string, day int) scan.FileInfo {
		return scan.FileInfo{
			AbsPath:     filepath.Join("/cam/DCIM", name),
			RelPath:     "DCIM/" + name,
			Size:        1024,
			CaptureTime: time.Date(2026, 3, day, 10, 0, 0, 0, time.Local),
		}
	}
	m.allFiles = []scan.FileInfo{
		file("DSC_0001.NEF", 24),
		file("DSC_0002.NEF", 25),
		file("DSC_0003.NEF", 25),
	}
	m.buildTree()
	m.rebuildVisible()
	return m
}

// rowAt describes the focused row as level plus its key, for readable failures.
func rowAt(m *Model) (int, string) {
	if len(m.visible) == 0 || m.cursor >= len(m.visible) {
		return -1, "<none>"
	}
	n := m.visible[m.cursor]
	if n.level == 3 {
		return 3, m.tree[n.year][n.month][n.day][n.fileIdx].RelPath
	}
	return n.level, nodeKey(n)
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// l walks in one level per press: year → month → day → file, opening anything
// closed on the way.
func TestEnterNode_WalksDown(t *testing.T) {
	m := navModel(t)
	m.expanded["2026"] = false
	m.cursor = 0

	m.handleMainKey(key("l")) // opens the year
	if lvl, k := rowAt(m); lvl != 0 || !m.expanded["2026"] {
		t.Fatalf("after first l: row %d %q, expanded=%v — want the year opened, cursor unmoved",
			lvl, k, m.expanded["2026"])
	}
	m.handleMainKey(key("l")) // steps onto the month
	if lvl, k := rowAt(m); lvl != 1 || k != "2026/2026-03" {
		t.Fatalf("after second l: row %d %q, want the month row", lvl, k)
	}
	m.handleMainKey(key("l")) // month is already open → onto the first day
	if lvl, k := rowAt(m); lvl != 2 || k != "2026/2026-03/2026-03-24" {
		t.Fatalf("after third l: row %d %q, want the first day", lvl, k)
	}
	m.handleMainKey(key("l")) // day is open → onto its first file
	if lvl, k := rowAt(m); lvl != 3 || k != "DCIM/DSC_0001.NEF" {
		t.Fatalf("after fourth l: row %d %q, want the first file", lvl, k)
	}
}

// l on a file opens the full preview, which is what enter does there too.
func TestEnterNode_FileOpensPreview(t *testing.T) {
	m := navModel(t)
	m.cursor = len(m.visible) - 1 // last file

	m.handleMainKey(key("l"))
	if m.screen != screenPreview {
		t.Fatalf("screen = %v, want the preview screen", m.screen)
	}
	if m.gridDay != "2026-03-25" || m.gridCursor != 1 {
		t.Errorf("preview opened on %s#%d, want 2026-03-25#1", m.gridDay, m.gridCursor)
	}
}

// h on a file closes the day it sits in and leaves the cursor on that day —
// the way out of a folder, not just a collapse.
func TestLeaveNode_FromFileClosesItsDay(t *testing.T) {
	m := navModel(t)
	for i, n := range m.visible {
		if n.level == 3 && n.day == "2026-03-25" {
			m.cursor = i
			break
		}
	}

	m.handleMainKey(key("h"))

	lvl, k := rowAt(m)
	if lvl != 2 || k != "2026/2026-03/2026-03-25" {
		t.Fatalf("row %d %q, want the day the file was in", lvl, k)
	}
	if m.expanded[k] {
		t.Error("the day is still open — h should have closed it")
	}
	for _, n := range m.visible {
		if n.level == 3 && n.day == "2026-03-25" {
			t.Fatal("files of the closed day are still listed")
		}
	}
}

// h on an open group closes it in place; a second h steps out to the parent.
func TestLeaveNode_ClosesThenWalksUp(t *testing.T) {
	m := navModel(t)
	for i, n := range m.visible {
		if n.level == 1 {
			m.cursor = i
			break
		}
	}

	m.handleMainKey(key("h"))
	if lvl, k := rowAt(m); lvl != 1 || m.expanded[k] {
		t.Fatalf("row %d, expanded=%v — want the month closed with the cursor on it", lvl, m.expanded[nodeKey(m.visible[m.cursor])])
	}

	m.handleMainKey(key("h"))
	if lvl, k := rowAt(m); lvl != 0 || k != "2026" || m.expanded["2026"] {
		t.Fatalf("row %d %q, expanded=%v — want the year row, closed", lvl, k, m.expanded["2026"])
	}
}

// A closed top-level row has nowhere to go: h must not move or crash.
func TestLeaveNode_AtTopLevelIsANoOp(t *testing.T) {
	m := navModel(t)
	m.expanded["2026"] = false
	m.rebuildVisible()
	m.cursor = 0

	m.handleMainKey(key("h"))
	if m.cursor != 0 || len(m.visible) != 1 {
		t.Errorf("cursor = %d, %d rows visible — want the single closed year, untouched", m.cursor, len(m.visible))
	}
}

// The arrow keys do the same as h/l.
func TestArrowKeysMatchHL(t *testing.T) {
	m := navModel(t)
	m.expanded["2026"] = false
	m.cursor = 0

	m.handleMainKey(tea.KeyMsg{Type: tea.KeyRight})
	if !m.expanded["2026"] {
		t.Error("right did not open the year")
	}
	m.handleMainKey(tea.KeyMsg{Type: tea.KeyLeft})
	if m.expanded["2026"] {
		t.Error("left did not close the year")
	}
}

// An empty tree (nothing missing anywhere) must not panic.
func TestTreeNav_EmptyTree(t *testing.T) {
	m := testModel(baseCfg(), "")
	m.handleMainKey(key("h"))
	m.handleMainKey(key("l"))
}
