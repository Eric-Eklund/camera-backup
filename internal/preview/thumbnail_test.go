package preview

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/tiff"
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

// ── the tag fallback chain, without exiftool or a real RAW file ───────────────

// encodeJPEG returns a decodable JPEG of the given size, standing in for an
// embedded preview.
func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// encodeTIFF stands in for NEF's ThumbnailTIFF, the one embedded image that is
// not a JPEG. It decodes only because x/image/tiff is registered.
func encodeTIFF(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := tiff.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestFirstDecodable_SkipsEmptyTags is the regression test for the NEF bug: the
// first tag asked for returns nothing, and the search must continue instead of
// reporting "no preview".
func TestFirstDecodable_SkipsEmptyTags(t *testing.T) {
	want := encodeJPEG(t, 64, 48)
	var asked []string
	// A Nikon NEF as exiftool actually reports it: no ThumbnailImage at all.
	nef := map[string][]byte{
		"-PreviewImage":   nil,
		"-ThumbnailImage": nil,
		"-JpgFromRaw":     want,
		"-ThumbnailTIFF":  encodeTIFF(t, 8, 8),
	}

	img, err := firstDecodable(
		[]string{"-PreviewImage", "-ThumbnailImage", "-JpgFromRaw", "-ThumbnailTIFF"},
		func(tag string) []byte { asked = append(asked, tag); return nef[tag] })
	if err != nil {
		t.Fatalf("firstDecodable: %v", err)
	}
	if img == nil {
		t.Fatal("gave up after the empty tags instead of trying the rest")
	}
	if got := img.Bounds(); got.Dx() != 64 || got.Dy() != 48 {
		t.Errorf("bounds = %v, want the JpgFromRaw image (64x48)", got)
	}
	// The search must stop as soon as a tag decodes.
	wantAsked := []string{"-PreviewImage", "-ThumbnailImage", "-JpgFromRaw"}
	if strings.Join(asked, ",") != strings.Join(wantAsked, ",") {
		t.Errorf("asked for %v, want %v", asked, wantAsked)
	}
}

// TestFirstDecodable_StopsAtFirstHit keeps a NEF to a single exiftool call in
// the common case: PreviewImage is present, so nothing else is fetched.
func TestFirstDecodable_StopsAtFirstHit(t *testing.T) {
	var asked []string
	img, err := firstDecodable(thumbPreviewTags, func(tag string) []byte {
		asked = append(asked, tag)
		if tag == "-PreviewImage" {
			return encodeJPEG(t, 32, 24)
		}
		return encodeJPEG(t, 640, 480)
	})
	if err != nil || img == nil {
		t.Fatalf("firstDecodable = %v, %v", img, err)
	}
	if len(asked) != 1 || asked[0] != "-PreviewImage" {
		t.Errorf("asked for %v, want a single -PreviewImage call", asked)
	}
}

// TestFirstDecodable_TIFFThumbnailDecodes covers the last-resort tag, which is
// the only reason x/image/tiff is registered as a decoder.
func TestFirstDecodable_TIFFThumbnailDecodes(t *testing.T) {
	only := map[string][]byte{"-ThumbnailTIFF": encodeTIFF(t, 160, 120)}
	img, err := firstDecodable(thumbPreviewTags, func(tag string) []byte { return only[tag] })
	if err != nil {
		t.Fatalf("firstDecodable: %v", err)
	}
	if img == nil {
		t.Fatal("ThumbnailTIFF did not decode — is the TIFF decoder registered?")
	}
	if got := img.Bounds(); got.Dx() != 160 {
		t.Errorf("bounds = %v, want the 160x120 TIFF", got)
	}
}

// TestFirstDecodable_UndecodableTagFallsThrough keeps one corrupt or
// unsupported embedded image from hiding a good one behind it.
func TestFirstDecodable_UndecodableTagFallsThrough(t *testing.T) {
	data := map[string][]byte{
		"-PreviewImage": []byte("not an image at all"),
		"-JpgFromRaw":   encodeJPEG(t, 96, 96),
	}
	img, err := firstDecodable(thumbPreviewTags, func(tag string) []byte { return data[tag] })
	if err != nil {
		t.Fatalf("firstDecodable: %v", err)
	}
	if img == nil || img.Bounds().Dx() != 96 {
		t.Fatalf("img = %v, want the decodable JpgFromRaw image", img)
	}
}

// TestFirstDecodable_NoneUsable reports the decode failure rather than a silent
// "unsupported", so a genuinely broken file is distinguishable.
func TestFirstDecodable_NoneUsable(t *testing.T) {
	img, err := firstDecodable(thumbPreviewTags, func(tag string) []byte {
		if tag == "-PreviewImage" {
			return []byte("garbage")
		}
		return nil
	})
	if img != nil {
		t.Fatalf("img = %v, want nil", img)
	}
	if err == nil {
		t.Error("want the decode error reported when no tag yields an image")
	}
}

// TestTagOrder pins the orderings the two callers depend on: thumbnails take
// the smallest adequate image, the full-screen preview the largest.
func TestTagOrder(t *testing.T) {
	if thumbPreviewTags[0] != "-PreviewImage" {
		t.Errorf("thumbnails should ask for -PreviewImage first, got %q — NEF needs one call",
			thumbPreviewTags[0])
	}
	if fullPreviewTags[0] != "-JpgFromRaw" {
		t.Errorf("full previews should ask for -JpgFromRaw first, got %q", fullPreviewTags[0])
	}
	if len(thumbPreviewTags) != len(fullPreviewTags) {
		t.Error("both orderings should cover the same tags")
	}
}

// TestExtractEmbeddedWithoutTool makes the missing-exiftool path explicit: RAW
// files report ErrNoRAWTool so the TUI can say what to install.
func TestExtractEmbeddedWithoutTool(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err == nil {
		t.Skip("exiftool is installed; this path needs it absent")
	}
	_, err := Thumbnail("/nonexistent/DSC_0001.NEF")
	if !errors.Is(err, ErrNoRAWTool) {
		t.Errorf("err = %v, want ErrNoRAWTool", err)
	}
}
