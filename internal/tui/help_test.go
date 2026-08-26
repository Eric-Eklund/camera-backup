package tui

import "testing"

func helpTestBlocks() [][]string {
	return [][]string{
		{"A", "a1", "a2"},
		{"B", "b1"},
		{"C", "c1", "c2"},
	}
}

// The reference is spaced out when it fits and packed together when it does
// not — a keybinding must never be pushed off screen for a blank line.
func TestHelpBody_DropsSpacingWhenTight(t *testing.T) {
	blocks := helpTestBlocks()
	if got := helpBody(blocks, 20); len(got) != 10 {
		t.Errorf("spaced body = %d lines, want 10: %q", len(got), got)
	}
	if got := helpBody(blocks, 9); len(got) != 8 {
		t.Errorf("packed body = %d lines, want 8: %q", len(got), got)
	}
}

func TestHelpWindow(t *testing.T) {
	lines := []string{"1", "2", "3", "4", "5"}

	win, off, more := helpWindow(lines, 0, 3)
	if len(win) != 3 || off != 0 || !more {
		t.Errorf("top window = %q off=%d more=%v, want the first 3 with more below", win, off, more)
	}

	// Scrolling past the end stops on the last screenful instead of running off
	// into blank space.
	win, off, more = helpWindow(lines, 99, 3)
	if off != 2 || win[len(win)-1] != "5" || more {
		t.Errorf("clamped window = %q off=%d more=%v, want the last 3 lines", win, off, more)
	}

	win, off, more = helpWindow(lines, -5, 10)
	if off != 0 || len(win) != 5 || more {
		t.Errorf("short list = %q off=%d more=%v, want everything", win, off, more)
	}
}

func TestScrollHelp_StaysInRange(t *testing.T) {
	m := testModel(baseCfg(), "")
	m.height, m.width = 24, 80

	m.scrollHelp(-1)
	if m.helpOffset != 0 {
		t.Errorf("helpOffset = %d after scrolling up from the top, want 0", m.helpOffset)
	}
	for i := 0; i < 100; i++ {
		m.scrollHelp(1)
	}
	total := len(helpBody(m.helpBlocks(), m.helpHeight()))
	if want := total - m.helpHeight(); m.helpOffset != want {
		t.Errorf("helpOffset = %d at the bottom, want %d", m.helpOffset, want)
	}
}
