package tui

import (
	"strings"
	"testing"

	"github.com/Eric-Eklund/lumen/internal/config"
)

func baseCfg() *config.Config {
	return &config.Config{
		Source:          "/cam",
		SSDPhotos:       "/ssd/Photos",
		SSDVideos:       "/ssd/Videos",
		NASPhotos:       "/nas/Photos",
		NASVideos:       "/nas/Videos",
		FileExtensions:  []string{".NEF", ".JPG", ".MOV"},
		VideoExtensions: []string{".MOV"},
	}
}

// setField sets a field by TOML key, failing the test if the key is not on the
// form — so a renamed key cannot silently make a test vacuous.
func setField(t *testing.T, f *settingsForm, key, value string) {
	t.Helper()
	for i := range f.fields {
		if f.fields[i].key == key {
			f.cursor = i
			f.setValue(value)
			return
		}
	}
	t.Fatalf("no settings field with key %q", key)
}

func fieldValue(t *testing.T, f *settingsForm, key string) string {
	t.Helper()
	for _, fl := range f.fields {
		if fl.key == key {
			return fl.value
		}
	}
	t.Fatalf("no settings field with key %q", key)
	return ""
}

// Opening the screen and saving without touching anything must not change the
// configuration.
func TestSettingsForm_RoundTripsUnchanged(t *testing.T) {
	cfg := baseCfg()
	f := newSettingsForm(cfg, "/tmp/config.toml")

	got, err := f.toConfig(cfg)
	if err != nil {
		t.Fatalf("toConfig: %v", err)
	}
	if got.Source != cfg.Source || got.SSDPhotos != cfg.SSDPhotos || got.NASVideos != cfg.NASVideos {
		t.Errorf("paths changed: %+v", got)
	}
	if got.DirectToNAS {
		t.Error("DirectToNAS flipped on")
	}
	if len(got.FileExtensions) != 3 {
		t.Errorf("FileExtensions = %v", got.FileExtensions)
	}
	// Optional numerics come back as explicit effective values.
	if got.SSDWorkerCount() != cfg.SSDWorkerCount() || got.NASWorkerCount() != cfg.NASWorkerCount() {
		t.Errorf("worker counts changed: %d/%d", got.SSDWorkerCount(), got.NASWorkerCount())
	}
	if got.SyncOrder() != cfg.SyncOrder() {
		t.Errorf("SyncOrder = %q, want %q", got.SyncOrder(), cfg.SyncOrder())
	}
	if f.dirty {
		t.Error("form is dirty without any edit")
	}
}

func TestSettingsForm_EffectiveValuesAreShown(t *testing.T) {
	f := newSettingsForm(baseCfg(), "/tmp/config.toml")
	for key, want := range map[string]string{
		"ssd_workers":               "3",
		"nas_workers":               "1",
		"nas_write_timeout_seconds": "60",
		"nas_sync_order":            config.OrderVideosFirst,
		"direct_to_nas":             boolOff,
	} {
		if got := fieldValue(t, f, key); got != want {
			t.Errorf("%s shows %q, want %q", key, got, want)
		}
	}
}

func TestSettingsForm_TogglesDirectToNAS(t *testing.T) {
	cfg := baseCfg()
	f := newSettingsForm(cfg, "/tmp/config.toml")

	for i := range f.fields {
		if f.fields[i].key == "direct_to_nas" {
			f.cursor = i
		}
	}
	f.startEdit() // a bool toggles in place instead of opening the editor
	if f.editing {
		t.Fatal("a bool field must not enter text edit mode")
	}
	if !f.dirty {
		t.Error("toggling did not mark the form dirty")
	}

	got, err := f.toConfig(cfg)
	if err != nil {
		t.Fatalf("toConfig: %v", err)
	}
	if !got.DirectToNAS {
		t.Error("DirectToNAS = false after toggle")
	}
}

// Dropping the SSD roots is only valid with direct_to_nas — the cross-key rule
// comes from config.Validate, and the form must surface it rather than saving.
func TestSettingsForm_RejectsSSDlessWithoutDirectMode(t *testing.T) {
	cfg := baseCfg()
	f := newSettingsForm(cfg, "/tmp/config.toml")
	setField(t, f, "ssd_photos", "")
	setField(t, f, "ssd_videos", "")

	if _, err := f.toConfig(cfg); err == nil {
		t.Fatal("expected an error with no SSD roots and direct_to_nas off")
	}

	// The same edit is fine once direct mode is on.
	setField(t, f, "direct_to_nas", boolOn)
	got, err := f.toConfig(cfg)
	if err != nil {
		t.Fatalf("toConfig with direct mode: %v", err)
	}
	if got.SSDConfigured() {
		t.Error("SSD roots were not cleared")
	}
}

func TestSettingsForm_RejectsEmptyRequiredField(t *testing.T) {
	cfg := baseCfg()
	f := newSettingsForm(cfg, "/tmp/config.toml")
	setField(t, f, "source", "   ")

	_, err := f.toConfig(cfg)
	if err == nil {
		t.Fatal("expected an error for an empty source")
	}
	if !strings.Contains(err.Error(), "Source device") {
		t.Errorf("error should name the field, got: %v", err)
	}
}

func TestSettingsForm_RejectsBadNumbers(t *testing.T) {
	cfg := baseCfg()
	for _, tc := range []struct{ key, value string }{
		{"ssd_workers", "many"},
		{"ssd_workers", "0"},
		{"nas_workers", "-2"},
		{"nas_write_timeout_seconds", "0"},
	} {
		f := newSettingsForm(cfg, "/tmp/config.toml")
		setField(t, f, tc.key, tc.value)
		if _, err := f.toConfig(cfg); err == nil {
			t.Errorf("%s = %q was accepted", tc.key, tc.value)
		}
	}
}

func TestSettingsForm_RejectsEmptyFileExtensions(t *testing.T) {
	cfg := baseCfg()
	f := newSettingsForm(cfg, "/tmp/config.toml")
	setField(t, f, "file_extensions", "")

	if _, err := f.toConfig(cfg); err == nil {
		t.Fatal("expected an error for an empty file_extensions")
	}
}

// A list field accepts sloppy input: stray spaces, trailing commas, missing dots.
func TestSettingsForm_NormalisesLists(t *testing.T) {
	cfg := baseCfg()
	f := newSettingsForm(cfg, "/tmp/config.toml")
	setField(t, f, "file_extensions", " NEF , .jpg,, MOV, ")
	setField(t, f, "extra_sources", "/media/a , /media/b,")

	got, err := f.toConfig(cfg)
	if err != nil {
		t.Fatalf("toConfig: %v", err)
	}
	wantExts := []string{".NEF", ".jpg", ".MOV"}
	if len(got.FileExtensions) != len(wantExts) {
		t.Fatalf("FileExtensions = %v, want %v", got.FileExtensions, wantExts)
	}
	for i := range wantExts {
		if got.FileExtensions[i] != wantExts[i] {
			t.Errorf("FileExtensions[%d] = %q, want %q", i, got.FileExtensions[i], wantExts[i])
		}
	}
	if len(got.ExtraSources) != 2 || got.ExtraSources[0] != "/media/a" || got.ExtraSources[1] != "/media/b" {
		t.Errorf("ExtraSources = %v", got.ExtraSources)
	}
}

func TestSettingsForm_EnumCycles(t *testing.T) {
	cfg := baseCfg()
	f := newSettingsForm(cfg, "/tmp/config.toml")
	for i := range f.fields {
		if f.fields[i].key == "nas_sync_order" {
			f.cursor = i
		}
	}
	f.startEdit()
	if got := fieldValue(t, f, "nas_sync_order"); got != config.OrderSizeAsc {
		t.Errorf("after one cycle = %q, want %q", got, config.OrderSizeAsc)
	}
	f.startEdit()
	if got := fieldValue(t, f, "nas_sync_order"); got != config.OrderVideosFirst {
		t.Errorf("after two cycles = %q, want %q", got, config.OrderVideosFirst)
	}
}

func TestSettingsForm_CancelEditRestoresValue(t *testing.T) {
	cfg := baseCfg()
	f := newSettingsForm(cfg, "/tmp/config.toml")
	f.cursor = 0 // source
	f.startEdit()
	f.editor.clear()
	f.editor.insert([]rune("/somewhere/else"))
	f.cancelEdit()

	if got := fieldValue(t, f, "source"); got != "/cam" {
		t.Errorf("source = %q, want the original /cam", got)
	}
	if f.editing {
		t.Error("still editing after cancel")
	}
}

func TestSettingsForm_CommitEditMarksDirty(t *testing.T) {
	cfg := baseCfg()
	f := newSettingsForm(cfg, "/tmp/config.toml")
	f.cursor = 0
	f.startEdit()
	f.editor.clear()
	f.editor.insert([]rune("/run/media/eric/NIKON"))
	f.commitEdit()

	if !f.dirty {
		t.Error("form is not dirty after an edit")
	}
	got, err := f.toConfig(cfg)
	if err != nil {
		t.Fatalf("toConfig: %v", err)
	}
	if got.Source != "/run/media/eric/NIKON" {
		t.Errorf("Source = %q", got.Source)
	}
}

func TestSettingsForm_CursorStaysInRange(t *testing.T) {
	f := newSettingsForm(baseCfg(), "/tmp/config.toml")
	f.moveCursor(-5)
	if f.cursor != 0 {
		t.Errorf("cursor = %d, want 0", f.cursor)
	}
	f.moveCursor(100)
	if f.cursor != len(f.fields)-1 {
		t.Errorf("cursor = %d, want %d", f.cursor, len(f.fields)-1)
	}
}

// ── line editor ───────────────────────────────────────────────────────────────

func TestLineEditor_Editing(t *testing.T) {
	e := newLineEditor("abc")
	if e.pos != 3 {
		t.Errorf("cursor starts at %d, want end (3)", e.pos)
	}
	e.insert([]rune("d"))
	if got := e.String(); got != "abcd" {
		t.Errorf("after insert = %q", got)
	}
	e.backspace()
	if got := e.String(); got != "abc" {
		t.Errorf("after backspace = %q", got)
	}
	e.home()
	e.deleteForward()
	if got := e.String(); got != "bc" {
		t.Errorf("after delete at home = %q", got)
	}
	e.insert([]rune("A"))
	if got := e.String(); got != "Abc" {
		t.Errorf("after insert at start = %q", got)
	}
	e.end()
	e.right() // already at the end — must not move past it
	if e.pos != len(e.String()) {
		t.Errorf("cursor ran past the end: %d", e.pos)
	}
	e.home()
	e.left() // already at the start
	if e.pos != 0 {
		t.Errorf("cursor ran before the start: %d", e.pos)
	}
	e.clear()
	if e.String() != "" || e.pos != 0 {
		t.Errorf("clear left %q at %d", e.String(), e.pos)
	}
	// Backspace on an empty buffer must be a no-op, not a panic.
	e.backspace()
	e.deleteForward()
}

func TestLineEditor_HandlesMultiByteRunes(t *testing.T) {
	e := newLineEditor("/mnt/förråd")
	e.backspace()
	if got := e.String(); got != "/mnt/förrå" {
		t.Errorf("backspace over a multi-byte rune gave %q", got)
	}
}

// A path longer than the column scrolls so the cursor stays on screen.
func TestLineEditor_RenderKeepsCursorVisible(t *testing.T) {
	e := newLineEditor("/run/media/eric/VERY-LONG-LABEL/DCIM")
	out := stripANSI(e.render(12))
	if len([]rune(out)) > 12 {
		t.Errorf("render(12) produced %d cells: %q", len([]rune(out)), out)
	}
	// The cursor is at the end, so the tail must be what is shown.
	if !strings.HasSuffix(strings.TrimRight(out, " "), "DCIM") {
		t.Errorf("window does not include the cursor: %q", out)
	}

	e.home()
	out = stripANSI(e.render(12))
	if !strings.HasPrefix(out, "/run/media/e") {
		t.Errorf("window did not scroll back to the start: %q", out)
	}
}

// stripANSI removes escape sequences so rendered text can be asserted on.
func stripANSI(s string) string {
	var sb strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape && (r == 'm' || r == 'K'):
			inEscape = false
		case !inEscape:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
