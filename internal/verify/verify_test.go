package verify_test

import (
	"io"
	"log"
	"os"
	"path/filepath"
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
	err := verify.RunWithCallback(cfg, logger, func(done, total int, r verify.FileResult) {
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
