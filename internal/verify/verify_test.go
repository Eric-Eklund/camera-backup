package verify_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
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
	outcome, err := verify.RunWithCallback(cfg, logger, func(done, total int, r verify.FileResult) {
		if len(r.Issues) > 0 {
			issues[r.RelPath] = r.Issues
		}
	})
	if err != nil {
		t.Fatalf("RunWithCallback: %v", err)
	}
	return issues, outcome.UnmountedRoots
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

// ── the cases the fast comparison cannot decide, and what verify says ────────

// writeWithCapture writes a JPEG carrying capture as DateTimeOriginal, padded
// with filler so two files can be made the same size with different content.
func writeWithCapture(t *testing.T, path, capture string, size int, filler byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	data := jpegWithDate(capture)
	for len(data) < size {
		data = append(data, filler)
	}
	if err := os.WriteFile(path, data[:size], 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modtime, modtime); err != nil {
		t.Fatal(err)
	}
}

// jpegWithDate builds a minimal JPEG whose APP1/Exif block holds date as
// DateTimeOriginal — enough for scan.CaptureTime to read it.
func jpegWithDate(date string) []byte {
	const (
		ifd0Off = 8
		exifOff = ifd0Off + 2 + 12 + 4
		dateOff = exifOff + 2 + 12 + 4
	)
	tiff := new(bytes.Buffer)
	tiff.WriteString("II*\x00")
	put32 := func(v uint32) { binary.Write(tiff, binary.LittleEndian, v) }
	put16 := func(v uint16) { binary.Write(tiff, binary.LittleEndian, v) }

	put32(ifd0Off)
	put16(1)
	put16(0x8769) // ExifIFDPointer
	put16(4)
	put32(1)
	put32(exifOff)
	put32(0)
	put16(1)
	put16(0x9003) // DateTimeOriginal
	put16(2)
	put32(20)
	put32(dateOff)
	put32(0)
	buf := make([]byte, 20)
	copy(buf, date)
	tiff.Write(buf)

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	out := new(bytes.Buffer)
	out.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	binary.Write(out, binary.BigEndian, uint16(len(payload)+2))
	out.Write(payload)
	return out.Bytes()
}

// TestVerify_DifferentShotSameNameSizeIsMissing is the case three SD cards
// create. The destination holds a *different* photograph that happens to share
// the basename and a byte-exact size, under another date. verify must call that
// "missing" — hashing a stranger and reporting a mismatch would describe the
// wrong problem, and skipping it would hide a photo that was never copied.
func TestVerify_DifferentShotSameNameSizeIsMissing(t *testing.T) {
	cfg, cam, ssd := setup(t)
	const size = 4096
	writeWithCapture(t, filepath.Join(cam, "DCIM/DSC_0020.JPG"), "2026:06:21 09:16:00", size, 0xAA)
	writeWithCapture(t, filepath.Join(ssd, "2026/2026-05/2026-05-10/DSC_0020.JPG"),
		"2026:05:10 14:31:00", size, 0xBB)

	issues := run(t, cfg)
	got := issues[filepath.ToSlash(filepath.Join("DCIM", "DSC_0020.JPG"))]
	if len(got) != 1 || got[0] != "missing from SSD" {
		t.Errorf("issues = %v, want [missing from SSD] — the copy is a different photograph", issues)
	}
}

// TestVerify_SameShotSameSizeIsFound is the other half: the same photograph
// under another date must still be recognised, not re-reported as missing.
func TestVerify_SameShotSameSizeIsFound(t *testing.T) {
	cfg, cam, ssd := setup(t)
	const (
		size = 4096
		shot = "2026:06:21 09:16:00"
	)
	writeWithCapture(t, filepath.Join(cam, "DCIM/DSC_0021.JPG"), shot, size, 0xAA)
	writeWithCapture(t, filepath.Join(ssd, "2026/2026-07/2026-07-28/DSC_0021.JPG"), shot, size, 0xAA)

	if issues := run(t, cfg); len(issues) != 0 {
		t.Errorf("issues = %v, want none — same shot, only filed under another date", issues)
	}
}

// TestVerify_CatchesSameDayNameSizeCollision covers the gap the fast comparison
// knowingly leaves open: two frames shot the *same day* with the same basename
// and a byte-exact size land on the same date path, so status cannot tell them
// apart. verify hashes, so it must.
func TestVerify_CatchesSameDayNameSizeCollision(t *testing.T) {
	cfg, cam, ssd := setup(t)
	const (
		size = 4096
		day  = "2026:03:25 10:00:00" // maps to datePath
	)
	writeWithCapture(t, filepath.Join(cam, "DCIM/DSC_0022.JPG"), day, size, 0xAA)
	writeWithCapture(t, filepath.Join(ssd, datePath, "DSC_0022.JPG"), day, size, 0xBB)

	issues := run(t, cfg)
	got := issues[filepath.ToSlash(filepath.Join("DCIM", "DSC_0022.JPG"))]
	if len(got) != 1 || got[0] != "SSD hash mismatch" {
		t.Errorf("issues = %v, want [SSD hash mismatch] — verify is the backstop here", issues)
	}
}

// TestCopyAndVerifyAgree locks the invariant the two paths are documented to
// share: a source that copy skips must be one verify considers present, and a
// source copy would transfer must be one verify reports as missing. They decide
// it separately — scan.MissingFromDest and verify.findCopy — so nothing but a
// test keeps them from drifting apart.
func TestCopyAndVerifyAgree(t *testing.T) {
	const (
		size     = 4096
		shot     = "2026:06:21 09:16:00"
		otherDay = "2026:05:10 14:31:00"
		sameDay  = "2026:03:25 10:00:00" // maps to datePath
	)
	cases := []struct {
		name string
		// place writes the destination copy, if there is one, under ssd.
		place func(t *testing.T, ssd string)
		// backedUp is the shared expectation: copy skips it and verify finds it.
		backedUp bool
	}{
		{
			name: "same shot at its own date",
			place: func(t *testing.T, ssd string) {
				writeWithCapture(t, filepath.Join(ssd, "2026/2026-06/2026-06-21/DSC_0030.JPG"), shot, size, 0xAA)
			},
			backedUp: true,
		},
		{
			name: "same shot under an older backup's date",
			place: func(t *testing.T, ssd string) {
				writeWithCapture(t, filepath.Join(ssd, "2026/2026-07/2026-07-28/DSC_0030.JPG"), shot, size, 0xAA)
			},
			backedUp: true,
		},
		{
			name: "different shot, same name and size, another date",
			place: func(t *testing.T, ssd string) {
				writeWithCapture(t, filepath.Join(ssd, "2026/2026-05/2026-05-10/DSC_0030.JPG"), otherDay, size, 0xBB)
			},
			backedUp: false,
		},
		{
			name:     "not on the destination at all",
			place:    func(t *testing.T, ssd string) {},
			backedUp: false,
		},
		{
			name: "same name at the right date but a different size",
			place: func(t *testing.T, ssd string) {
				writeWithCapture(t, filepath.Join(ssd, "2026/2026-06/2026-06-21/DSC_0030.JPG"), shot, size/2, 0xAA)
			},
			backedUp: false,
		},
		{
			name: "same day, same name and size, different photograph",
			place: func(t *testing.T, ssd string) {
				writeWithCapture(t, filepath.Join(ssd, datePath, "DSC_0030.JPG"), sameDay, size, 0xBB)
			},
			// The fast comparison accepts this; only verify can tell, and it
			// reports a hash mismatch rather than "missing".
			backedUp: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, cam, ssd := setup(t)
			capture := shot
			if tc.name == "same day, same name and size, different photograph" {
				capture = sameDay
			}
			writeWithCapture(t, filepath.Join(cam, "DCIM/DSC_0030.JPG"), capture, size, 0xAA)
			tc.place(t, ssd)

			// What copy would do.
			srcFiles, _, err := scan.WalkSource(cam, cfg.NormalisedExtensions())
			if err != nil {
				t.Fatal(err)
			}
			dstFiles, _ := scan.WalkDual(cfg.SSDPhotos, cfg.SSDVideos, cfg.NormalisedExtensions())
			wouldCopy := len(scan.MissingFromDest(srcFiles, scan.IndexByRelPath(dstFiles))) > 0

			// What verify says.
			issues := run(t, cfg)
			var reportedMissing bool
			for _, is := range issues {
				for _, s := range is {
					if s == "missing from SSD" {
						reportedMissing = true
					}
				}
			}

			if wouldCopy == tc.backedUp {
				t.Errorf("copy wouldCopy = %v, want %v", wouldCopy, !tc.backedUp)
			}
			if reportedMissing == tc.backedUp {
				t.Errorf("verify reportedMissing = %v, want %v", reportedMissing, !tc.backedUp)
			}
			if wouldCopy != reportedMissing {
				t.Errorf("copy and verify disagree: wouldCopy = %v, verify missing = %v (issues %v)",
					wouldCopy, reportedMissing, issues)
			}
		})
	}
}
