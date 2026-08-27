package tui

// First-run mode: `lumen tui` on a machine with no config.toml opens on the
// settings screen, the first successful save creates the file and continues
// into the normal flow, and quitting before saving writes nothing. These
// tests pin that contract — in particular that no file appears unless the
// user explicitly saved one.

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Eric-Eklund/lumen/internal/config"
)

// escKey complements tree_nav_test's key helper, which only covers rune keys.
func escKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEsc} }

func newFirstRunModel(t *testing.T) (*Model, string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	m := NewFirstRun(config.FirstRunDefaults(), log.New(io.Discard, "", 0), configPath)
	return m, configPath
}

func TestFirstRun_OpensOnTheSettingsScreen(t *testing.T) {
	m, _ := newFirstRunModel(t)

	if m.screen != screenSettings {
		t.Fatalf("screen = %v, want the settings screen", m.screen)
	}
	if m.settings == nil {
		t.Fatal("no settings form — there is nothing to fill in")
	}
	// The template's extension lists are pre-filled; only the paths are the
	// user's to provide.
	if got := fieldValue(t, m.settings, "file_extensions"); got == "" {
		t.Error("file_extensions is empty — the defaults should be seeded")
	}
}

// Quitting before the first save is a clean exit that writes nothing: an
// aborted first run must not leave a half-made config.toml behind for the
// next start to load.
func TestFirstRun_QuitWithoutSavingWritesNothing(t *testing.T) {
	m, configPath := newFirstRunModel(t)

	_, cmd := m.handleKey(key("q"))
	if cmd == nil {
		t.Fatal("q returned no command, want quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q = %T, want tea.QuitMsg — there is no main screen to fall back to", cmd())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("config.toml exists after quitting without saving (stat: %v)", err)
	}
}

// With unsaved edits the first esc warns, exactly as on the normal settings
// screen; only the second one quits.
func TestFirstRun_DirtyFormTakesTwoEscapesToQuit(t *testing.T) {
	m, _ := newFirstRunModel(t)
	setField(t, m.settings, "source", "/run/media/user/CARD")

	_, cmd := m.handleKey(escKey())
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("the first esc quit despite unsaved changes")
		}
	}
	if !m.settings.confirmExit {
		t.Fatal("the first esc did not arm the unsaved-changes confirmation")
	}

	_, cmd = m.handleKey(escKey())
	if cmd == nil {
		t.Fatal("the second esc returned no command, want quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("second esc = %T, want tea.QuitMsg", cmd())
	}
}

// The first successful save creates config.toml and continues into the normal
// flow: first-run mode ends and the scan starts, exactly as a start with that
// config would have.
func TestFirstRun_SaveCreatesTheConfigAndContinues(t *testing.T) {
	m, configPath := newFirstRunModel(t)
	dir := t.TempDir()
	setField(t, m.settings, "source", filepath.Join(dir, "card"))
	setField(t, m.settings, "ssd_photos", filepath.Join(dir, "ssd", "photos"))
	setField(t, m.settings, "ssd_videos", filepath.Join(dir, "ssd", "videos"))

	cmd := m.saveSettings()
	if cmd == nil {
		t.Fatal("saveSettings returned no command — the rescan never starts")
	}
	if m.firstRun {
		t.Error("firstRun is still set after a successful save")
	}
	if m.screen != screenLoading {
		t.Errorf("screen = %v, want the loading screen — the save continues into the scan", m.screen)
	}

	// The file it wrote is a config a fresh start would accept.
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("the saved config does not load: %v", err)
	}
	if cfg.Source != filepath.Join(dir, "card") {
		t.Errorf("source = %q, want the value typed into the form", cfg.Source)
	}
}

// An invalid draft is refused exactly as on the normal settings screen: the
// error is shown, nothing is written, and first-run mode continues.
func TestFirstRun_InvalidDraftSavesNothing(t *testing.T) {
	m, configPath := newFirstRunModel(t)
	// source deliberately left empty — Validate refuses it.

	_ = m.saveSettings()

	if !m.firstRun {
		t.Error("firstRun ended although nothing was saved")
	}
	if m.settings == nil || m.settings.err == "" {
		t.Error("no error shown for a draft that cannot be saved")
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("config.toml exists after a refused save (stat: %v)", err)
	}
}
