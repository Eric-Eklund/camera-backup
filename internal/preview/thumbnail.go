package preview

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	// Nikon stores the small embedded preview of a NEF as an uncompressed TIFF
	// (exiftool's ThumbnailTIFF), so the TIFF decoder has to be registered for
	// the last-resort fallback below to decode.
	_ "golang.org/x/image/tiff"
)

// ErrNoRAWTool is returned when a RAW file's preview is requested but exiftool
// is not in PATH. Callers can surface it to explain the missing thumbnail
// instead of silently showing nothing.
var ErrNoRAWTool = errors.New("exiftool not found in PATH — RAW previews unavailable")

// rawExts are the extensions whose preview is extracted with exiftool.
var rawExts = map[string]struct{}{
	".nef": {}, ".nrw": {}, ".cr2": {}, ".cr3": {}, ".arw": {}, ".dng": {},
	".orf": {}, ".rw2": {}, ".raf": {}, ".pef": {}, ".srw": {}, ".raw": {},
}

// thumbPreviewTags lists the embedded images tried when building a small
// thumbnail, cheapest adequate image first.
//
// Nikon NEF files have *no* ThumbnailImage tag — asking exiftool for
// -ThumbnailImage returns zero bytes. The small JPEG lives in PreviewImage
// (~640 px, ~100 kB), the full-resolution JPEG in JpgFromRaw (~1 MB), and the
// tiny reduced-resolution image in IFD0 is an uncompressed TIFF reported as
// ThumbnailTIFF. Trying PreviewImage first therefore succeeds in one exiftool
// call for NEF/CR2/ARW/ORF alike, and keeps the decoded image small.
var thumbPreviewTags = []string{"-PreviewImage", "-ThumbnailImage", "-JpgFromRaw", "-ThumbnailTIFF"}

// fullPreviewTags lists the same images ordered largest-first, for the
// full-screen preview where resolution matters more than decode cost.
var fullPreviewTags = []string{"-JpgFromRaw", "-PreviewImage", "-ThumbnailImage", "-ThumbnailTIFF"}

// Thumbnail decodes a small preview image from absPath.
// JPEG/PNG are decoded directly; RAW files go through exiftool.
// Returns (nil, nil) for unsupported types (video), and (nil, ErrNoRAWTool)
// for a RAW file when exiftool is unavailable.
func Thumbnail(absPath string) (image.Image, error) {
	switch ext := strings.ToLower(filepath.Ext(absPath)); {
	case ext == ".jpg", ext == ".jpeg", ext == ".png":
		return decodeFile(absPath)
	case isRAW(ext):
		return extractEmbedded(absPath, thumbPreviewTags)
	default:
		// Video and other unsupported types.
		return nil, nil
	}
}

// FullImage decodes the best available full-size preview of absPath.
// JPEG/PNG are decoded directly. For RAW files it prefers the embedded
// full-resolution JPEG and falls back to the smaller previews.
// Returns (nil, nil) for unsupported types, and (nil, ErrNoRAWTool) for a RAW
// file when exiftool is unavailable.
func FullImage(absPath string) (image.Image, error) {
	switch ext := strings.ToLower(filepath.Ext(absPath)); {
	case ext == ".jpg", ext == ".jpeg", ext == ".png":
		return decodeFile(absPath)
	case isRAW(ext):
		return extractEmbedded(absPath, fullPreviewTags)
	default:
		return nil, nil
	}
}

// isRAW reports whether ext (lowercased, with leading dot) is a RAW format
// whose preview must be extracted with exiftool.
func isRAW(ext string) bool {
	_, ok := rawExts[ext]
	return ok
}

func decodeFile(absPath string) (image.Image, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// extractEmbedded tries each tag in order and returns the first embedded image
// that decodes. A tag that is absent (empty output) or holds data this build
// cannot decode is skipped rather than failing the whole lookup.
func extractEmbedded(absPath string, tags []string) (image.Image, error) {
	tool, err := exiftoolPath()
	if err != nil {
		return nil, ErrNoRAWTool
	}
	var firstErr error
	for _, tag := range tags {
		img, err := extractRAWTag(tool, absPath, tag)
		if img != nil {
			return img, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// extractRAWTag runs `exiftool -b <tag>` and decodes the result.
// A missing tag yields (nil, nil) so the caller can try the next one.
func extractRAWTag(tool, absPath, tag string) (image.Image, error) {
	out, err := exec.Command(tool, "-b", tag, absPath).Output()
	if err != nil || len(out) == 0 {
		return nil, nil
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		return nil, err
	}
	return img, nil
}

var (
	exiftoolOnce sync.Once
	exiftoolBin  string
	exiftoolErr  error
)

// exiftoolPath locates exiftool once per process — Thumbnail is called for
// every visible file in the grid, and a failed PATH lookup is not free.
func exiftoolPath() (string, error) {
	exiftoolOnce.Do(func() {
		exiftoolBin, exiftoolErr = exec.LookPath("exiftool")
	})
	return exiftoolBin, exiftoolErr
}

// RAWToolAvailable reports whether exiftool is in PATH, i.e. whether RAW
// previews can be produced at all.
func RAWToolAvailable() bool {
	_, err := exiftoolPath()
	return err == nil
}
