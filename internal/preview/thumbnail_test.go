package preview

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireExiftool skips when exiftool is unavailable — RAW previews cannot be
// produced without it, and that is a supported (if degraded) configuration.
func requireExiftool(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not in PATH")
	}
}

// rawSample returns the path of a RAW sample from CAMERA_BACKUP_RAW_SAMPLES,
// a directory of real camera files kept outside the repo (they are megabytes
// each). Skips when it is not set.
func rawSample(t *testing.T, name string) string {
	t.Helper()
	dir := os.Getenv("CAMERA_BACKUP_RAW_SAMPLES")
	if dir == "" {
		t.Skip("set CAMERA_BACKUP_RAW_SAMPLES to a directory of RAW samples")
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("sample %s not present", name)
	}
	return p
}

// TestThumbnailNEF is the regression test for NEF thumbnails coming back nil.
// Nikon RAW files have no ThumbnailImage tag, so asking exiftool for that tag
// alone returned zero bytes and neither the list view nor the grid ever showed
// a preview.
func TestThumbnailNEF(t *testing.T) {
	requireExiftool(t)
	for _, name := range []string{"glarus.nef", "philadelphia.nef"} {
		t.Run(name, func(t *testing.T) {
			path := rawSample(t, name)

			img, err := Thumbnail(path)
			if err != nil {
				t.Fatalf("Thumbnail: %v", err)
			}
			if img == nil {
				t.Fatal("Thumbnail returned nil for a NEF with embedded previews")
			}
			if b := img.Bounds(); b.Dx() < 100 || b.Dy() < 100 {
				t.Errorf("thumbnail suspiciously small: %v", b)
			}

			full, err := FullImage(path)
			if err != nil {
				t.Fatalf("FullImage: %v", err)
			}
			if full == nil {
				t.Fatal("FullImage returned nil for a NEF with embedded previews")
			}
			// FullImage prefers JpgFromRaw, so it must not be smaller than the
			// thumbnail chain's pick.
			if full.Bounds().Dx() < img.Bounds().Dx() {
				t.Errorf("FullImage %v smaller than Thumbnail %v", full.Bounds(), img.Bounds())
			}
		})
	}
}

// TestThumbnailUnsupported keeps video and unknown extensions cheap: no
// exiftool call, no error, just "nothing to show".
func TestThumbnailUnsupported(t *testing.T) {
	for _, name := range []string{"clip.mov", "clip.mp4", "notes.txt"} {
		img, err := Thumbnail(filepath.Join(t.TempDir(), name))
		if img != nil || err != nil {
			t.Errorf("Thumbnail(%s) = %v, %v; want nil, nil", name, img, err)
		}
	}
}

// TestThumbnailBadJPEG surfaces a decode failure rather than pretending the
// file simply has no preview.
func TestThumbnailBadJPEG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncated.jpg")
	if err := os.WriteFile(path, []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Thumbnail(path); err == nil {
		t.Error("Thumbnail on a truncated JPEG returned no error")
	}
}
