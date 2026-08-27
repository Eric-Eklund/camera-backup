package tui

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Eric-Eklund/lumen/internal/config"
	"github.com/Eric-Eklund/lumen/internal/devices"
	"github.com/Eric-Eklund/lumen/internal/status"
)

func testLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func testModel(cfg *config.Config, configPath string) *Model {
	m := New(cfg, testLogger(), configPath)
	m.screen = screenMain
	return m
}

// Picking a device is the hot swap: it becomes the source immediately, the
// screen goes back to the file list, and a rescan is issued.
func TestUseDevice_SwapsSourceAndRescans(t *testing.T) {
	card := t.TempDir()
	drive := t.TempDir()
	cfg := baseCfg()
	cfg.Source = card
	m := testModel(cfg, "")
	m.selected = map[string]bool{"/old/file.NEF": true}
	m.picker = newDevicePicker(pickerSwap, card, "")

	cmd := m.useDevice(devices.Device{Path: drive, Label: "EXT-SSD"})

	if got := m.cfg.ActiveSource(); got != drive {
		t.Errorf("ActiveSource() = %q, want the picked device %q", got, drive)
	}
	if cmd == nil {
		t.Error("no command returned — nothing would rescan the new device")
	}
	if m.screen != screenMain || m.picker != nil {
		t.Errorf("screen = %v, picker = %v, want the main screen with the picker closed", m.screen, m.picker)
	}
	if len(m.selected) != 0 {
		t.Errorf("selection = %v, want it cleared — it pointed at the previous device", m.selected)
	}
	if !strings.Contains(m.statusMsg, "EXT-SSD") {
		t.Errorf("statusMsg = %q, want it to name the device being read", m.statusMsg)
	}
	// config.toml is untouched: a swapped card is a session choice.
	if m.cfg.Source != card {
		t.Errorf("Source = %q, want the configured %q left alone", m.cfg.Source, card)
	}
}

func TestSaveDeviceAsSource_KeepsOldSourceAsExtra(t *testing.T) {
	card := t.TempDir()
	drive := t.TempDir()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	cfg := baseCfg()
	cfg.Source = card
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}

	m := testModel(cfg, configPath)
	m.picker = newDevicePicker(pickerSwap, card, "")
	m.cfg.SetSourceOverride(drive) // picked earlier this session

	if cmd := m.saveDeviceAsSource(devices.Device{Path: drive, Label: "EXT-SSD"}); cmd == nil {
		t.Error("no command returned — nothing would rescan after the save")
	}
	if m.picker.err != "" {
		t.Fatalf("save reported: %s", m.picker.err)
	}

	saved, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Source != drive {
		t.Errorf("saved source = %q, want %q", saved.Source, drive)
	}
	if len(saved.ExtraSources) != 1 || saved.ExtraSources[0] != card {
		t.Errorf("saved extra_sources = %v, want the previous source %q kept", saved.ExtraSources, card)
	}
	// The override would be a second, invisible answer to a question
	// config.toml now answers.
	if m.cfg.SourceOverride() != "" {
		t.Errorf("SourceOverride() = %q, want it cleared after saving", m.cfg.SourceOverride())
	}
	if got := m.cfg.ActiveSource(); got != drive {
		t.Errorf("ActiveSource() = %q, want %q", got, drive)
	}
}

func TestSaveDeviceAsSource_NoConfigFile(t *testing.T) {
	m := testModel(baseCfg(), "")
	m.picker = newDevicePicker(pickerSwap, "/cam", "")

	if cmd := m.saveDeviceAsSource(devices.Device{Path: "/media/card"}); cmd != nil {
		t.Error("a command was returned although the save could not happen")
	}
	if m.picker.err == "" {
		t.Error("no error shown for a session started without a config file")
	}
}

// The picker opens on the device in use, so [enter] on an untouched list is a
// no-op rather than a surprise swap.
func TestPickerApply_FocusesActiveDevice(t *testing.T) {
	p := newDevicePicker(pickerSwap, "/run/media/eric/EXT-SSD", "")
	p.apply([]devices.Device{
		{Path: "/run/media/eric/CAMERA-CARD"},
		{Path: "/run/media/eric/EXT-SSD"},
		{Path: "/"},
	}, nil)

	if p.loading {
		t.Error("still loading after a result arrived")
	}
	d, ok := p.current()
	if !ok || d.Path != "/run/media/eric/EXT-SSD" {
		t.Errorf("focused %+v, want the active device", d)
	}
}

func TestPickerApply_Error(t *testing.T) {
	p := newDevicePicker(pickerSwap, "", "")
	p.apply(nil, os.ErrPermission)
	if p.loading || p.err == "" {
		t.Errorf("loading = %v, err = %q, want a finished scan reporting the failure", p.loading, p.err)
	}
	if _, ok := p.current(); ok {
		t.Error("current() returned a device from an empty list")
	}
}

func TestPickerMoveCursor_StaysInRange(t *testing.T) {
	p := newDevicePicker(pickerSwap, "", "")
	p.moveCursor(1) // empty list
	if p.cursor != 0 {
		t.Errorf("cursor = %d on an empty list, want 0", p.cursor)
	}
	p.apply([]devices.Device{{Path: "/a"}, {Path: "/b"}}, nil)
	p.moveCursor(5)
	if p.cursor != 1 {
		t.Errorf("cursor = %d, want the last row", p.cursor)
	}
	p.moveCursor(-5)
	if p.cursor != 0 {
		t.Errorf("cursor = %d, want the first row", p.cursor)
	}
}

// In field mode the picker fills a settings row instead of swapping the source.
func TestFillSettingsPath_SingleAndList(t *testing.T) {
	cfg := baseCfg()
	m := testModel(cfg, "/tmp/config.toml")
	m.settings = newSettingsForm(cfg, "/tmp/config.toml")
	m.picker = newDevicePicker(pickerField, cfg.Source, "Source device")
	m.screen = screenDevices

	setField(t, m.settings, "source", "")
	m.fillSettingsPath("/run/media/eric/CAMERA-CARD")

	if m.screen != screenSettings || m.picker != nil {
		t.Errorf("screen = %v, picker = %v, want a return to the settings form", m.screen, m.picker)
	}
	if got := fieldValue(t, m.settings, "source"); got != "/run/media/eric/CAMERA-CARD" {
		t.Errorf("source field = %q, want the picked path", got)
	}
	if m.cfg.SourceOverride() != "" {
		t.Error("filling a settings field must not swap the running source")
	}

	setField(t, m.settings, "extra_sources", "/media/a")
	m.picker = newDevicePicker(pickerField, "", "Extra sources")
	m.fillSettingsPath("/media/b")
	if got := fieldValue(t, m.settings, "extra_sources"); got != "/media/a, /media/b" {
		t.Errorf("extra_sources = %q, want the picked path appended", got)
	}

	// Picking the same device twice must not duplicate it.
	m.picker = newDevicePicker(pickerField, "", "Extra sources")
	m.fillSettingsPath("/media/b")
	if got := fieldValue(t, m.settings, "extra_sources"); got != "/media/a, /media/b" {
		t.Errorf("extra_sources = %q, want no duplicate", got)
	}
}

// Destination roots are fixed mount points typed once; only the source fields
// take a device from the list.
func TestCanPickDevice_OnlySourceFields(t *testing.T) {
	f := newSettingsForm(baseCfg(), "/tmp/config.toml")
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"source", true},
		{"extra_sources", true},
		{"nas_photos", false},
		{"ssd_workers", false},
	} {
		setField(t, f, tc.key, fieldValue(t, f, tc.key))
		if got := f.canPickDevice(); got != tc.want {
			t.Errorf("canPickDevice() on %s = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// [d] on the main screen opens the picker and starts a scan.
func TestMainKey_D_OpensPicker(t *testing.T) {
	m := testModel(baseCfg(), "")
	_, cmd := m.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	if m.screen != screenDevices || m.picker == nil {
		t.Fatalf("screen = %v, picker = %v, want the device screen open", m.screen, m.picker)
	}
	if !m.picker.loading || m.picker.mode != pickerSwap {
		t.Errorf("picker = %+v, want a loading swap picker", m.picker)
	}
	if cmd == nil {
		t.Error("no scan command returned")
	}
}

// A device event while the picker is open refreshes the list — a card going in
// or coming out is exactly what the screen is showing.
func TestDeviceChanged_RefreshesOpenPicker(t *testing.T) {
	m := testModel(baseCfg(), "")
	m.picker = newDevicePicker(pickerSwap, "", "")
	m.picker.apply([]devices.Device{{Path: "/a"}}, nil)
	m.screen = screenDevices

	_, cmd := m.Update(deviceChangedMsg{})
	if cmd == nil {
		t.Fatal("no command returned — the stale list would stay on screen")
	}
	if !m.picker.loading {
		t.Error("picker not marked loading")
	}
	if m.screen != screenDevices {
		t.Errorf("screen = %v, want to stay on the device screen", m.screen)
	}
}

// A status scan landing while the picker is open must not yank the user back
// to the file list.
func TestStatusReady_KeepsPickerOpen(t *testing.T) {
	m := testModel(baseCfg(), "")
	m.picker = newDevicePicker(pickerSwap, "", "")
	m.screen = screenDevices

	m.Update(statusReadyMsg{result: &status.StatusResult{}})
	if m.screen != screenDevices {
		t.Errorf("screen = %v, want to stay on the device screen", m.screen)
	}
}
