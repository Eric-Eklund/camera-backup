package verify_test

import (
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/verify"
)

// directSetup builds a card and a NAS for a direct_to_nas run, with the SSD
// keys still set — the combination config-template.toml recommends, since
// `sync` keeps working with them and only `copy` changes behaviour.
func directSetup(t *testing.T) (cfg *config.Config, cam, ssd, nas string) {
	t.Helper()
	cam, ssd, nas = t.TempDir(), t.TempDir(), t.TempDir()
	cfg = &config.Config{
		Source:          cam,
		SSDPhotos:       ssd,
		SSDVideos:       ssd,
		NASPhotos:       nas,
		NASVideos:       nas,
		DirectToNAS:     true,
		FileExtensions:  []string{".jpg", ".mov"},
		VideoExtensions: []string{".mov"},
	}
	return cfg, cam, ssd, nas
}

func outcomeOf(t *testing.T, cfg *config.Config) (map[string][]string, verify.Outcome) {
	t.Helper()
	issues := map[string][]string{}
	outcome, err := verify.RunWithCallback(cfg, log.New(io.Discard, "", 0),
		func(done, total int, r verify.FileResult) {
			if len(r.Issues) > 0 {
				issues[r.RelPath] = r.Issues
			}
		})
	if err != nil {
		t.Fatalf("RunWithCallback: %v", err)
	}
	return issues, outcome
}

// The false alarm this fixes. In direct mode nothing is ever written to the
// SSD, so expecting copies there reported a flawless dump as almost entirely
// broken — six failures out of seven, all "missing from SSD". A verify people
// learn to disregard is not a safety net.
func TestVerify_DirectModeDoesNotExpectCopiesOnTheBypassedSSD(t *testing.T) {
	cfg, cam, _, nas := directSetup(t)

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0001.JPG"), "a photograph")
	writeFile(t, filepath.Join(cam, "DCIM/VID_0001.MOV"), "a clip")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0001.JPG"), "a photograph")
	writeFile(t, filepath.Join(nas, datePath, "VID_0001.MOV"), "a clip")

	issues, outcome := outcomeOf(t, cfg)
	if len(issues) != 0 {
		t.Errorf("issues = %v, want none — every file is on the NAS, which is the whole backup in direct mode", issues)
	}
	if !outcome.Clean() {
		t.Errorf("outcome = %+v, want clean", outcome)
	}
}

// The bypassed SSD is not named as an unchecked destination either: it is not
// a destination of this run at all, and listing it would send the user off to
// mount something that would change nothing.
func TestVerify_DirectModeDoesNotNameTheSSDAsUnchecked(t *testing.T) {
	cfg, cam, ssd, nas := directSetup(t)
	if err := os.RemoveAll(ssd); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cam, "DCIM/DSC_0001.JPG"), "a photograph")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0001.JPG"), "a photograph")

	_, outcome := outcomeOf(t, cfg)
	for _, r := range outcome.UnmountedRoots {
		if strings.Contains(r, "SSD") {
			t.Errorf("UnmountedRoots = %v, want no SSD entry in direct mode", outcome.UnmountedRoots)
		}
	}
}

// A genuinely missing NAS copy must still be reported in direct mode — the fix
// must not have made verify quiet, only correct.
func TestVerify_DirectModeStillReportsAMissingNASCopy(t *testing.T) {
	cfg, cam, _, nas := directSetup(t)

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0001.JPG"), "a photograph")
	writeFile(t, filepath.Join(cam, "DCIM/DSC_0002.JPG"), "another photograph")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0001.JPG"), "a photograph")

	issues, _ := outcomeOf(t, cfg)
	got := issues[filepath.ToSlash(filepath.Join("DCIM", "DSC_0002.JPG"))]
	if len(got) != 1 || got[0] != "missing from NAS" {
		t.Fatalf("issues = %v, want DSC_0002.JPG missing from NAS", issues)
	}
	if len(issues) != 1 {
		t.Errorf("issues = %v, want exactly the one genuinely missing file", issues)
	}
}

// A corrupt NAS copy is still caught: the hashes are what verify is for.
func TestVerify_DirectModeStillCatchesAHashMismatch(t *testing.T) {
	cfg, cam, _, nas := directSetup(t)

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0001.JPG"), "the original")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0001.JPG"), "the corrupt.")

	issues, _ := outcomeOf(t, cfg)
	got := issues[filepath.ToSlash(filepath.Join("DCIM", "DSC_0001.JPG"))]
	if len(got) != 1 || got[0] != "NAS hash mismatch" {
		t.Fatalf("issues = %v, want a NAS hash mismatch", issues)
	}
}

// With no card mounted, direct mode has no authority at all: the SSD is not
// part of the pipeline, so standing it in would verify a tree this
// configuration never writes to. The error has to say which of the two it is.
func TestVerify_DirectModeWithoutASourceHasNoAuthority(t *testing.T) {
	cfg, cam, _, _ := directSetup(t)
	if err := os.RemoveAll(cam); err != nil {
		t.Fatal(err)
	}

	_, err := verify.RunWithCallback(cfg, log.New(io.Discard, "", 0), nil)
	if err == nil {
		t.Fatal("verify ran without an authority in direct mode")
	}
	if !strings.Contains(err.Error(), "direct_to_nas") {
		t.Errorf("error = %v, want it to explain that the SSD is bypassed", err)
	}
}

// The staged pipeline is untouched: with direct_to_nas off, the SSD is a real
// destination and a missing copy there is a real finding.
func TestVerify_StagedModeStillChecksTheSSD(t *testing.T) {
	cfg, cam, _, nas := directSetup(t)
	cfg.DirectToNAS = false

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0001.JPG"), "a photograph")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0001.JPG"), "a photograph")

	issues, _ := outcomeOf(t, cfg)
	got := issues[filepath.ToSlash(filepath.Join("DCIM", "DSC_0001.JPG"))]
	if len(got) != 1 || got[0] != "missing from SSD" {
		t.Fatalf("issues = %v, want the SSD copy reported missing in staged mode", issues)
	}
}

// And with no card, staged mode still falls back to the SSD as authority.
func TestVerify_StagedModeWithoutASourceUsesTheSSD(t *testing.T) {
	cfg, cam, ssd, nas := directSetup(t)
	cfg.DirectToNAS = false

	writeFile(t, filepath.Join(ssd, datePath, "DSC_0001.JPG"), "a photograph")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0001.JPG"), "a photograph")
	if err := os.RemoveAll(cam); err != nil {
		t.Fatal(err)
	}

	issues, outcome := outcomeOf(t, cfg)
	if len(issues) != 0 {
		t.Errorf("issues = %v, want none", issues)
	}
	if !outcome.Clean() {
		t.Errorf("outcome = %+v, want clean", outcome)
	}
}

// Outcome.Clean is what the CLI and the TUI branch on, so its meaning is
// pinned here rather than left to each caller to re-derive.
func TestOutcome_CleanOnlyWhenNothingWentUnlookedAt(t *testing.T) {
	if !(verify.Outcome{}).Clean() {
		t.Error("an empty Outcome should be clean")
	}
	if (verify.Outcome{UnmountedRoots: []string{"NAS (/mnt/nas)"}}).Clean() {
		t.Error("an unmounted destination is not a clean pass")
	}
	unreadable := []scan.Unreadable{{Path: "/card/DCIM", Err: fs.ErrPermission}}
	if (verify.Outcome{Unreadable: unreadable}).Clean() {
		t.Error("an unreadable source path is not a clean pass")
	}
}
