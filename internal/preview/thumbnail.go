package preview

import (
	"bytes"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Thumbnail decodes a preview image from absPath.
// JPEG/PNG are decoded directly. NEF/CR2/ARW shell out to exiftool -b -ThumbnailImage.
// Returns (nil, nil) for unsupported types (video) or if exiftool is not in PATH.
func Thumbnail(absPath string) (image.Image, error) {
	ext := strings.ToLower(filepath.Ext(absPath))
	switch ext {
	case ".jpg", ".jpeg", ".png":
		return decodeFile(absPath)
	case ".nef", ".cr2", ".arw", ".dng", ".orf", ".rw2", ".raf":
		return extractRAWThumbnail(absPath)
	default:
		// Video and other unsupported types.
		return nil, nil
	}
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

func extractRAWThumbnail(absPath string) (image.Image, error) {
	exiftool, err := exec.LookPath("exiftool")
	if err != nil {
		// exiftool not installed — no preview available.
		return nil, nil
	}
	out, err := exec.Command(exiftool, "-b", "-ThumbnailImage", absPath).Output()
	if err != nil || len(out) == 0 {
		return nil, nil
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	return img, err
}
