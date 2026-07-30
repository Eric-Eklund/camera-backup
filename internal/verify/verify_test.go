package verify_test

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/verify"
)

var (
	modtime  = time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	datePath = filepath.Join("2026", "2026-03", "2026-03-25")
)

// setup creates camera and SSD roots and returns a config pointing at them
// (merged SSD root, no NAS).
func setup(t *testing.T) (*config.Config, string, string) {
	t.Helper()
	cam, ssd := t.TempDir(), t.TempDir()
	cfg := &config.Config{
		Source:          cam,
		SSDPhotos:       ssd,
		SSDVideos:       ssd,
		FileExtensions:  []string{".jpg", ".mov"},
		VideoExtensions: []string{".mov"},
	}
	return cfg, cam, ssd
}

// writeFile writes content at path (creating parents) with the fixed modtime,
// so camera files map to datePath on the destinations.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modtime, modtime); err != nil {
		t.Fatal(err)
	}
}

// run verifies and returns issues keyed by camera RelPath (only files with issues).
func run(t *testing.T, cfg *config.Config) map[string][]string {
	t.Helper()
	logger := log.New(io.Discard, "", 0)
	issues := map[string][]string{}
	_, err := verify.RunWithCallback(cfg, logger, func(done, total int, r verify.FileResult) {
		if len(r.Issues) > 0 {
			issues[r.RelPath] = r.Issues
		}
	})
	if err != nil {
		t.Fatalf("RunWithCallback: %v", err)
	}
	return issues
}

func TestVerify_AllOK(t *testing.T) {
	cfg, cam, ssd := setup(t)
	writeFile(t, filepath.Join(cam, "DCIM/DSC_0001.JPG"), "identical content")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0001.JPG"), "identical content")

	if issues := run(t, cfg); len(issues) != 0 {
		t.Errorf("issues = %v, want none", issues)
	}
}

func TestVerify_CollisionCopyMatches(t *testing.T) {
	// A name collision was resolved by saving the camera file as _1.
	// Verify must check _1 — not report a false mismatch against the original.
	cfg, cam, ssd := setup(t)
	writeFile(t, filepath.Join(cam, "DCIM/DSC_0002.JPG"), "camera version AAAA")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0002.JPG"), "older different file")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0002_1.JPG"), "camera version AAAA")

	if issues := run(t, cfg); len(issues) != 0 {
		t.Errorf("issues = %v, want none (collision copy _1 matches)", issues)
	}
}

func TestVerify_SameSizeCorruptionReported(t *testing.T) {
	cfg, cam, ssd := setup(t)
	writeFile(t, filepath.Join(cam, "DCIM/DSC_0003.JPG"), "aaaa")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0003.JPG"), "bbbb") // same size, different bytes

	issues := run(t, cfg)
	got := issues["DCIM/DSC_0003.JPG"]
	if len(got) != 1 || got[0] != "SSD hash mismatch" {
		t.Errorf("issues = %v, want [SSD hash mismatch]", got)
	}
}

func TestVerify_SizeMismatchWithoutCopyIsMissing(t *testing.T) {
	// Only a stray file of a different size exists (e.g. a partial copy) —
	// the camera file is missing from the SSD, not corrupt.
	cfg, cam, ssd := setup(t)
	writeFile(t, filepath.Join(cam, "DCIM/DSC_0004.JPG"), "full camera content")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0004.JPG"), "partial")

	issues := run(t, cfg)
	got := issues["DCIM/DSC_0004.JPG"]
	if len(got) != 1 || got[0] != "missing from SSD" {
		t.Errorf("issues = %v, want [missing from SSD]", got)
	}
}

func TestVerify_MissingFromSSD(t *testing.T) {
	cfg, cam, _ := setup(t)
	writeFile(t, filepath.Join(cam, "DCIM/DSC_0005.JPG"), "not copied yet")

	issues := run(t, cfg)
	got := issues["DCIM/DSC_0005.JPG"]
	if len(got) != 1 || got[0] != "missing from SSD" {
		t.Errorf("issues = %v, want [missing from SSD]", got)
	}
}

func TestVerify_NASCollisionCopyMatches(t *testing.T) {
	// Same collision scenario on the NAS side.
	cfg, cam, ssd := setup(t)
	nas := t.TempDir()
	cfg.NASPhotos, cfg.NASVideos = nas, nas

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0006.JPG"), "camera version BBBB")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0006.JPG"), "camera version BBBB")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0006.JPG"), "older different file")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0006_1.JPG"), "camera version BBBB")

	if issues := run(t, cfg); len(issues) != 0 {
		t.Errorf("issues = %v, want none (NAS collision copy _1 matches)", issues)
	}
}

func TestVerify_SSDAuthorityWhenCameraAbsent(t *testing.T) {
	// No camera: SSD is the authority and is compared against the NAS.
	cfg, _, ssd := setup(t)
	cfg.Source = filepath.Join(ssd, "nonexistent-camera")
	nas := t.TempDir()
	cfg.NASPhotos, cfg.NASVideos = nas, nas

	writeFile(t, filepath.Join(ssd, datePath, "DSC_0007.JPG"), "synced content")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0007.JPG"), "synced content")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0008.JPG"), "only on ssd")

	issues := run(t, cfg)
	if len(issues) != 1 {
		t.Fatalf("issues = %v, want exactly one file flagged", issues)
	}
	got := issues[filepath.ToSlash(filepath.Join(datePath, "DSC_0008.JPG"))]
	if len(got) != 1 || got[0] != "missing from NAS" {
		t.Errorf("issues = %v, want [missing from NAS]", issues)
	}
}

// TestVerify_CopyUnderDifferentDatePath covers backups whose copy does not sit
// under the date the camera file resolves to now — the case every existing
// backup landed in once files started being filed by capture date instead of
// modtime. verify must find such a copy by basename+size, the same way a copy
// run decides the file is already present, or every previously backed-up file
// would be reported missing.
func TestVerify_CopyUnderDifferentDatePath(t *testing.T) {
	cfg, cam, ssd := setup(t)
	const content = "shot years before it was copied"
	otherDate := filepath.Join("2012", "2012-08", "2012-08-05")

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0009.JPG"), content)
	writeFile(t, filepath.Join(ssd, otherDate, "DSC_0009.JPG"), content)

	if issues := run(t, cfg); len(issues) != 0 {
		t.Errorf("issues = %v, want none — the copy exists, just under another date", issues)
	}
}

// TestVerify_DifferentDateDifferentSizeIsMissing guards the fallback above from
// hiding a genuinely absent file: a same-name copy of a different size is not a
// match, wherever in the tree it sits.
func TestVerify_DifferentDateDifferentSizeIsMissing(t *testing.T) {
	cfg, cam, ssd := setup(t)
	otherDate := filepath.Join("2012", "2012-08", "2012-08-05")

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0010.JPG"), "the real thing")
	writeFile(t, filepath.Join(ssd, otherDate, "DSC_0010.JPG"), "a truncated copy")

	issues := run(t, cfg)
	if len(issues) != 1 {
		t.Fatalf("issues = %v, want the file flagged", issues)
	}
	got := issues[filepath.ToSlash(filepath.Join("DCIM", "DSC_0010.JPG"))]
	if len(got) != 1 || got[0] != "missing from SSD" {
		t.Errorf("issues = %v, want [missing from SSD]", issues)
	}
}

// runFull is like run but also returns the destinations verify could not check.
func runFull(t *testing.T, cfg *config.Config) (map[string][]string, []string) {
	t.Helper()
	logger := log.New(io.Discard, "", 0)
	issues := map[string][]string{}
	skipped, err := verify.RunWithCallback(cfg, logger, func(done, total int, r verify.FileResult) {
		if len(r.Issues) > 0 {
			issues[r.RelPath] = r.Issues
		}
	})
	if err != nil {
		t.Fatalf("RunWithCallback: %v", err)
	}
	return issues, skipped
}

// TestVerify_ReportsUnmountedDestination is the honesty guard: a pass that never
// looked at the NAS must not read as a clean bill of health for it.
func TestVerify_ReportsUnmountedDestination(t *testing.T) {
	cfg, cam, ssd := setup(t)
	missingRoot := filepath.Join(t.TempDir(), "not-mounted")
	cfg.NASPhotos = filepath.Join(missingRoot, "Photos")
	cfg.NASVideos = filepath.Join(missingRoot, "Videos")

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0011.JPG"), "content")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0011.JPG"), "content")

	issues, skipped := runFull(t, cfg)
	if len(issues) != 0 {
		t.Errorf("issues = %v, want none — the SSD copy is fine", issues)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v, want both NAS roots reported", skipped)
	}
	for _, s := range skipped {
		if !strings.Contains(s, "NAS") || !strings.Contains(s, missingRoot) {
			t.Errorf("skipped entry %q should name the device and its path", s)
		}
	}
}

// TestVerify_MergedRootReportedOnce keeps the message readable when both
// categories point at one directory.
func TestVerify_MergedRootReportedOnce(t *testing.T) {
	cfg, cam, ssd := setup(t)
	// The parent must be gone too: a root whose parent exists counts as
	// available, because the root itself is created on first copy.
	merged := filepath.Join(t.TempDir(), "not-mounted", "share")
	cfg.NASPhotos, cfg.NASVideos = merged, merged

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0012.JPG"), "content")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0012.JPG"), "content")

	_, skipped := runFull(t, cfg)
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want the merged NAS root named once", skipped)
	}
}

// TestVerify_NothingSkippedWhenAllMounted keeps the clean case clean — the
// warning must not appear when there is nothing to warn about.
func TestVerify_NothingSkippedWhenAllMounted(t *testing.T) {
	cfg, cam, ssd := setup(t)
	nas := t.TempDir()
	cfg.NASPhotos, cfg.NASVideos = nas, nas

	writeFile(t, filepath.Join(cam, "DCIM/DSC_0013.JPG"), "content")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0013.JPG"), "content")
	writeFile(t, filepath.Join(nas, datePath, "DSC_0013.JPG"), "content")

	issues, skipped := runFull(t, cfg)
	if len(issues) != 0 || len(skipped) != 0 {
		t.Errorf("issues = %v, skipped = %v, want both empty", issues, skipped)
	}
}

// TestVerify_UnconfiguredDeviceNotReported distinguishes "not set up" from
// "set up but not mounted" — only the latter is a gap in the check.
func TestVerify_UnconfiguredDeviceNotReported(t *testing.T) {
	cfg, cam, ssd := setup(t) // no NAS keys at all
	writeFile(t, filepath.Join(cam, "DCIM/DSC_0014.JPG"), "content")
	writeFile(t, filepath.Join(ssd, datePath, "DSC_0014.JPG"), "content")

	if _, skipped := runFull(t, cfg); len(skipped) != 0 {
		t.Errorf("skipped = %v, want none — no NAS is configured", skipped)
	}
}
