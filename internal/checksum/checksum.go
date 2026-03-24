package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// File computes the SHA256 hex digest of the file at path.
// The file is opened read-only.
func File(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return "", fmt.Errorf("checksum: open %q: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("checksum: reading %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FileWithProgress computes SHA256 while also writing read bytes into w.
// This lets the caller display progress during hashing.
func FileWithProgress(path string, w io.Writer) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return "", fmt.Errorf("checksum: open %q: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	mw := io.MultiWriter(h, w)
	if _, err := io.Copy(mw, f); err != nil {
		return "", fmt.Errorf("checksum: reading %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
