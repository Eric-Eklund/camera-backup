package checksum_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eric-Eklund/camera-backup/internal/checksum"
)

func TestFile_ReturnsSHA256Hex(t *testing.T) {
	path := writeTempFile(t, []byte("hello world"))

	h, err := checksum.File(path)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	// SHA256 hex is always 64 characters.
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64", len(h))
	}
}

func TestFile_SameContentSameHash(t *testing.T) {
	content := []byte("identical content")
	h1, _ := checksum.File(writeTempFile(t, content))
	h2, _ := checksum.File(writeTempFile(t, content))

	if h1 != h2 {
		t.Error("identical content produced different hashes")
	}
}

func TestFile_DifferentContentDifferentHash(t *testing.T) {
	h1, _ := checksum.File(writeTempFile(t, []byte("content A")))
	h2, _ := checksum.File(writeTempFile(t, []byte("content B")))

	if h1 == h2 {
		t.Error("different content produced same hash")
	}
}

func TestFile_MissingFile(t *testing.T) {
	if _, err := checksum.File("/nonexistent/file.bin"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFileWithProgress_MatchesFile(t *testing.T) {
	content := []byte("test content for progress")
	path := writeTempFile(t, content)

	h1, _ := checksum.File(path)

	var buf bytes.Buffer
	h2, err := checksum.FileWithProgress(path, &buf)
	if err != nil {
		t.Fatalf("FileWithProgress: %v", err)
	}

	if h1 != h2 {
		t.Errorf("File hash %q != FileWithProgress hash %q", h1, h2)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Error("progress writer did not receive all file bytes")
	}
}

func TestFileWithProgress_MissingFile(t *testing.T) {
	var buf bytes.Buffer
	if _, err := checksum.FileWithProgress("/nonexistent/file.bin", &buf); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func writeTempFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.bin")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}
