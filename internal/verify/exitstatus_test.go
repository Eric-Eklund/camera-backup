package verify

// These tests are about verify's exit status. The printed summary is for
// whoever is watching the terminal; a cron job running the README's monthly
// verify, or a `verify && notify` chain, sees only the returned error — and a
// verify that found mismatches used to return nil, reporting a corrupt backup
// as confirmed to everything that could not read the summary.

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
)

func TestRunError_MismatchesAreAnError(t *testing.T) {
	err := runError(2, 7, Outcome{})
	if err == nil {
		t.Fatal("runError = nil for a pass with 2 failing files")
	}
	for _, want := range []string{"2 of 7", "not confirmed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestRunError_UnreadableSourceIsAnError(t *testing.T) {
	outcome := Outcome{Unreadable: []scan.Unreadable{
		{Path: "/card/DCIM/101NIKON", Err: errors.New("permission denied")},
	}}
	err := runError(0, 7, outcome)
	if err == nil {
		t.Fatal("runError = nil although part of the source was never read")
	}
	for _, want := range []string{"could not be read", "do not format the card"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// An unmounted destination alone keeps exit 0: skipping it is the documented
// "skipped, not failed" behaviour — verifying camera vs SSD away from the NAS
// is a normal run — and the summary already names what went unchecked.
func TestRunError_UnmountedDestinationAloneIsNotAnError(t *testing.T) {
	outcome := Outcome{UnmountedRoots: []string{"NAS photos (/mnt/nas/Photos)"}}
	if err := runError(0, 7, outcome); err != nil {
		t.Errorf("runError = %v, want nil for an unmounted destination", err)
	}
}

func TestRunError_CleanPassIsNil(t *testing.T) {
	if err := runError(0, 7, Outcome{}); err != nil {
		t.Errorf("runError = %v, want nil", err)
	}
}

// ── Run itself, end to end ──────────────────────────────────────────────────

var exitModtime = time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)

func writeExitFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, exitModtime, exitModtime); err != nil {
		t.Fatal(err)
	}
}

func exitSetup(t *testing.T) (*config.Config, string, string) {
	t.Helper()
	cam, ssd := t.TempDir(), t.TempDir()
	return &config.Config{
		Source:          cam,
		SSDPhotos:       ssd,
		SSDVideos:       ssd,
		FileExtensions:  []string{".jpg"},
		VideoExtensions: []string{".mov"},
	}, cam, ssd
}

// A same-size corruption is exactly what verify exists to catch, and catching
// it has to reach the exit status — the summary line alone is invisible to a
// script.
func TestRun_HashMismatchExitsNonZero(t *testing.T) {
	cfg, cam, ssd := exitSetup(t)
	datePath := filepath.Join("2026", "2026-03", "2026-03-25")
	writeExitFile(t, filepath.Join(cam, "DCIM/DSC_0001.JPG"), "aaaa")
	writeExitFile(t, filepath.Join(ssd, datePath, "DSC_0001.JPG"), "bbbb")

	err := Run(cfg, log.New(io.Discard, "", 0), false)
	if err == nil {
		t.Fatal("Run returned nil although a file failed verification")
	}
	if !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("error = %v, want it to say the backup is not confirmed", err)
	}
}

func TestRun_CleanPassExitsZero(t *testing.T) {
	cfg, cam, ssd := exitSetup(t)
	datePath := filepath.Join("2026", "2026-03", "2026-03-25")
	writeExitFile(t, filepath.Join(cam, "DCIM/DSC_0001.JPG"), "identical")
	writeExitFile(t, filepath.Join(ssd, datePath, "DSC_0001.JPG"), "identical")

	if err := Run(cfg, log.New(io.Discard, "", 0), false); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
