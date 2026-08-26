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
//
// HEIF stills (Canon writes .HIF, an iPhone .HEIC) are in the list because Go
// cannot decode them either, and a camera-written HEIF usually carries an
// embedded JPEG preview that exiftool can pull out. Where there is none — an
// iPhone frame, most AVIF — the panel says so, exactly as it does for a video.
var rawExts = map[string]struct{}{
	".nef": {}, ".nrw": {}, ".cr2": {}, ".cr3": {}, ".arw": {}, ".dng": {},
	".orf": {}, ".rw2": {}, ".raf": {}, ".pef": {}, ".srw": {}, ".raw": {},
	".heic": {}, ".heif": {}, ".hif": {}, ".avif": {},
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
// that decodes.
func extractEmbedded(absPath string, tags []string) (image.Image, error) {
	tool, err := exiftoolPath()
	if err != nil {
		return nil, ErrNoRAWTool
	}
	return firstDecodable(tags, func(tag string) []byte {
		out, err := exec.Command(tool, "-b", tag, absPath).Output()
		if err != nil {
			return nil
		}
		return out
	})
}

// firstDecodable walks tags in order and returns the first one whose bytes
// decode as an image. A tag that is absent (get returns nothing, which is what
// exiftool does for NEF's ThumbnailImage) or that holds data this build cannot
// decode must not end the search — giving up on the first empty tag is exactly
// why NEF thumbnails were blank. The first decode error is reported only when
// no tag produced an image at all.
func firstDecodable(tags []string, get func(tag string) []byte) (image.Image, error) {
	var firstErr error
	for _, tag := range tags {
		out := get(tag)
		if len(out) == 0 {
			continue
		}
		img, _, err := image.Decode(bytes.NewReader(out))
		if err == nil {
			return img, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
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
