package main

import (
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
)

// These tests are about exit status, which is the only thing a cron job, a
// systemd timer or `camera-backup sync && notify-send done` can see. A command
// that prints a warning and returns nil tells all of them that the backup
// finished.

var testModtime = time.Date(2026, 3, 25, 10, 0, 0, 0, time.Local)

// silentLogger keeps the tests quiet; the commands under test also write to
// stdout, which go test only shows for a failure.
func silentLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// syncFixture builds an SSD holding one file, plus a NAS root, and the config
// pointing at both.
type syncFixture struct {
	cfg      *config.Config
	ssd, nas string
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	dir := t.TempDir()
	f := &syncFixture{
		ssd: filepath.Join(dir, "ssd"),
		nas: filepath.Join(dir, "nas"),
	}
	datePath := filepath.Join("2026", "2026-03", "2026-03-25")
	writeAt(t, filepath.Join(f.ssd, datePath, "DSC_0001.NEF"), "a photograph")
	if err := os.MkdirAll(f.nas, 0o755); err != nil {
		t.Fatal(err)
	}
	f.cfg = &config.Config{
		Source:          filepath.Join(dir, "card"),
		SSDPhotos:       f.ssd,
		SSDVideos:       f.ssd,
		NASPhotos:       filepath.Join(f.nas, "photos"),
		NASVideos:       filepath.Join(f.nas, "videos"),
		FileExtensions:  []string{".NEF", ".MOV"},
		VideoExtensions: []string{".MOV"},
	}
	return f
}

func writeAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, testModtime, testModtime); err != nil {
		t.Fatal(err)
	}
}

// blockDestination replaces the NAS photos root's parent path component with a
// regular file, so creating the date directories under it fails with ENOTDIR.
// A permission bit would not do: root ignores those, and this has to fail for
// whoever runs the suite.
func blockDestination(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A sync where every file failed used to exit 0 — it printed a warning and
// returned nil, so a script chaining on success carried on as if the NAS were
// up to date.
func TestRunSync_FailuresExitNonZero(t *testing.T) {
	f := newSyncFixture(t)
	blockDestination(t, f.cfg.NASPhotos)

	err := runSync(f.cfg, silentLogger(), syncOptions{order: config.OrderVideosFirst})
	if err == nil {
		t.Fatal("runSync returned nil after every file failed")
	}
	if !strings.Contains(err.Error(), "SSD → NAS") {
		t.Errorf("error = %v, want it to name the phase that failed", err)
	}
}

// A sync that copies everything still succeeds — the fix must not turn a good
// run into a failure.
func TestRunSync_SucceedsWhenEverythingCopies(t *testing.T) {
	f := newSyncFixture(t)

	if err := runSync(f.cfg, silentLogger(), syncOptions{order: config.OrderVideosFirst}); err != nil {
		t.Fatalf("runSync: %v", err)
	}
	want := filepath.Join(f.cfg.NASPhotos, "2026", "2026-03", "2026-03-25", "DSC_0001.NEF")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the file did not reach the NAS: %v", err)
	}
}

// An unreachable NAS is not a failure: the documented behaviour is that copy
// finishes after phase 1 and the user runs sync again later. That has to keep
// working, or every laptop-away-from-home backup starts exiting non-zero.
func TestRunSync_UnavailableNASIsNotAFailure(t *testing.T) {
	f := newSyncFixture(t)
	unmounted := filepath.Join(t.TempDir(), "not-mounted", "share")
	f.cfg.NASPhotos = filepath.Join(unmounted, "photos")
	f.cfg.NASVideos = filepath.Join(unmounted, "videos")

	if err := runSync(f.cfg, silentLogger(), syncOptions{order: config.OrderVideosFirst}); err != nil {
		t.Errorf("runSync = %v, want nil — an unmounted NAS is a 'try again later', not a failure", err)
	}
}

// Nothing to do is a success.
func TestRunSync_NothingToCopySucceeds(t *testing.T) {
	f := newSyncFixture(t)
	if err := runSync(f.cfg, silentLogger(), syncOptions{order: config.OrderVideosFirst}); err != nil {
		t.Fatalf("first runSync: %v", err)
	}
	if err := runSync(f.cfg, silentLogger(), syncOptions{order: config.OrderVideosFirst}); err != nil {
		t.Errorf("second runSync = %v, want nil — everything is already there", err)
	}
}

// ── The unreadable-source exit status ───────────────────────────────────────

func TestIncompleteSourceError(t *testing.T) {
	if err := incompleteSourceError(nil); err != nil {
		t.Errorf("incompleteSourceError(nil) = %v, want nil", err)
	}

	err := incompleteSourceError([]scan.Unreadable{
		{Path: "/card/DCIM/101NIKON", Err: fs.ErrPermission},
		{Path: "/card/DCIM/102NIKON", Err: fs.ErrPermission},
	})
	if err == nil {
		t.Fatal("incompleteSourceError returned nil for two unreadable paths")
	}
	msg := err.Error()
	for _, want := range []string{"2 source path(s)", "/card/DCIM/101NIKON", "/card/DCIM/102NIKON", "do not format the card"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

// A direct dump whose scan could not read part of the card copies what it can
// and then exits non-zero. Copying what is reachable is right — those files are
// worth having — but the run must not end on a success.
func TestRunDirect_UnreadableSourceCopiesWhatItCanAndStillFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	dir := t.TempDir()
	card := filepath.Join(dir, "card")
	nas := filepath.Join(dir, "nas")

	writeAt(t, filepath.Join(card, "DCIM", "100NIKON", "DSC_0001.NEF"), "a readable photograph")
	locked := filepath.Join(card, "DCIM", "101NIKON")
	writeAt(t, filepath.Join(locked, "DSC_0002.NEF"), "a photograph nobody can see")
	if err := os.MkdirAll(nas, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	cfg := &config.Config{
		Source:          card,
		NASPhotos:       filepath.Join(nas, "photos"),
		NASVideos:       filepath.Join(nas, "videos"),
		DirectToNAS:     true,
		FileExtensions:  []string{".NEF"},
		VideoExtensions: []string{".MOV"},
	}

	err := runDirect(cfg, silentLogger(), syncOptions{order: config.OrderVideosFirst})
	if err == nil {
		t.Fatal("runDirect succeeded although part of the card could not be read")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error = %v, want it to say the backup is incomplete", err)
	}

	// The file it could read is on the NAS all the same.
	copied := filepath.Join(cfg.NASPhotos, "2026", "2026-03", "2026-03-25", "DSC_0001.NEF")
	if _, statErr := os.Stat(copied); statErr != nil {
		t.Errorf("the readable file was not copied: %v", statErr)
	}
}

// With nothing left to copy the run is still a failure if part of the card went
// unread — "already up to date" is exactly the reassuring line that must not
// appear on its own.
func TestRunDirect_UnreadableSourceFailsEvenWithNothingToCopy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	dir := t.TempDir()
	card := filepath.Join(dir, "card")
	nas := filepath.Join(dir, "nas")

	locked := filepath.Join(card, "DCIM")
	writeAt(t, filepath.Join(locked, "DSC_0002.NEF"), "a photograph nobody can see")
	if err := os.MkdirAll(filepath.Join(nas, "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	cfg := &config.Config{
		Source:          card,
		NASPhotos:       filepath.Join(nas, "photos"),
		NASVideos:       filepath.Join(nas, "videos"),
		DirectToNAS:     true,
		FileExtensions:  []string{".NEF"},
		VideoExtensions: []string{".MOV"},
	}

	if err := runDirect(cfg, silentLogger(), syncOptions{order: config.OrderVideosFirst}); err == nil {
		t.Fatal("runDirect reported success for a card it could only partly read")
	}
}

// A card that was read in full ends on a plain success.
func TestRunDirect_FullyReadableSourceSucceeds(t *testing.T) {
	dir := t.TempDir()
	card := filepath.Join(dir, "card")
	nas := filepath.Join(dir, "nas")

	writeAt(t, filepath.Join(card, "DCIM", "100NIKON", "DSC_0001.NEF"), "a photograph")
	if err := os.MkdirAll(nas, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Source:          card,
		NASPhotos:       filepath.Join(nas, "photos"),
		NASVideos:       filepath.Join(nas, "videos"),
		DirectToNAS:     true,
		FileExtensions:  []string{".NEF"},
		VideoExtensions: []string{".MOV"},
	}

	if err := runDirect(cfg, silentLogger(), syncOptions{order: config.OrderVideosFirst}); err != nil {
		t.Fatalf("runDirect: %v", err)
	}
	if err := runDirect(cfg, silentLogger(), syncOptions{order: config.OrderVideosFirst}); err != nil {
		t.Errorf("second runDirect = %v, want nil — nothing left to copy", err)
	}
}
