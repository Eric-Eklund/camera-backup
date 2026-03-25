// White-box tests — same package so we can reach unexported safeCreate.
package copyop

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/scan"
)

// ── safeCreate ────────────────────────────────────────────────────────────────

func TestSafeCreate_NewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "DSC_0001.NEF")

	f, got, err := safeCreate(path)
	if err != nil {
		t.Fatalf("safeCreate: %v", err)
	}
	f.Close()

	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file was not created: %v", err)
	}
}

func TestSafeCreate_CollisionAdds1Suffix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DSC_0001.NEF")
	os.WriteFile(path, []byte("original"), 0644)

	f, got, err := safeCreate(path)
	if err != nil {
		t.Fatalf("safeCreate: %v", err)
	}
	f.Close()

	want := filepath.Join(dir, "DSC_0001_1.NEF")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	// Original must be untouched.
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Error("original file was modified")
	}
}

func TestSafeCreate_MultipleCollisions(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "DSC_0001.NEF")
	os.WriteFile(base, []byte("v0"), 0644)
	os.WriteFile(filepath.Join(dir, "DSC_0001_1.NEF"), []byte("v1"), 0644)

	f, got, err := safeCreate(base)
	if err != nil {
		t.Fatalf("safeCreate: %v", err)
	}
	f.Close()

	want := filepath.Join(dir, "DSC_0001_2.NEF")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestSafeCreate_NoExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noext")
	os.WriteFile(path, []byte("x"), 0644)

	f, got, err := safeCreate(path)
	if err != nil {
		t.Fatalf("safeCreate: %v", err)
	}
	f.Close()

	want := filepath.Join(dir, "noext_1")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// ── CopyAndVerify ─────────────────────────────────────────────────────────────

func TestCopyAndVerify_Success(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	content := []byte("fake nef data")
	srcFile := filepath.Join(src, "DSC_0001.NEF")
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	os.WriteFile(srcFile, content, 0644)
	os.Chtimes(srcFile, modtime, modtime)

	task := Task{
		Src:        scan.FileInfo{AbsPath: srcFile, RelPath: "DCIM/DSC_0001.NEF", Size: int64(len(content)), ModTime: modtime},
		DstRelPath: "photos/2026-03-25/DSC_0001.NEF",
	}

	if err := CopyAndVerify(task, dst, logger); err != nil {
		t.Fatalf("CopyAndVerify: %v", err)
	}

	dstFile := filepath.Join(dst, "photos/2026-03-25/DSC_0001.NEF")

	got, _ := os.ReadFile(dstFile)
	if string(got) != string(content) {
		t.Error("destination content does not match source")
	}

	fi, _ := os.Stat(dstFile)
	if fi.ModTime().Unix() != modtime.Unix() {
		t.Errorf("modtime = %v, want %v", fi.ModTime(), modtime)
	}
}

func TestCopyAndVerify_NeverOverwrites(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	logger := log.New(io.Discard, "", 0)
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)

	// New camera file.
	content := []byte("new camera file")
	srcFile := filepath.Join(src, "DSC_0001.NEF")
	os.WriteFile(srcFile, content, 0644)
	os.Chtimes(srcFile, modtime, modtime)

	// Pre-existing file at destination with different content.
	dstDir := filepath.Join(dst, "photos/2026-03-25")
	os.MkdirAll(dstDir, 0755)
	existing := filepath.Join(dstDir, "DSC_0001.NEF")
	os.WriteFile(existing, []byte("existing file"), 0644)

	task := Task{
		Src:        scan.FileInfo{AbsPath: srcFile, RelPath: "DCIM/DSC_0001.NEF", Size: int64(len(content)), ModTime: modtime},
		DstRelPath: "photos/2026-03-25/DSC_0001.NEF",
	}

	if err := CopyAndVerify(task, dst, logger); err != nil {
		t.Fatalf("CopyAndVerify: %v", err)
	}

	// Original must be untouched.
	orig, _ := os.ReadFile(existing)
	if string(orig) != "existing file" {
		t.Error("existing destination file was overwritten")
	}

	// New copy must exist as _1.
	collision := filepath.Join(dstDir, "DSC_0001_1.NEF")
	got, _ := os.ReadFile(collision)
	if string(got) != string(content) {
		t.Errorf("collision file content = %q, want %q", string(got), string(content))
	}
}

func TestCopyAndVerify_MissingSource(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	task := Task{
		Src:        scan.FileInfo{AbsPath: "/nonexistent/DSC_0001.NEF", RelPath: "DSC_0001.NEF", Size: 100, ModTime: time.Now()},
		DstRelPath: "photos/2026-03-25/DSC_0001.NEF",
	}
	if err := CopyAndVerify(task, t.TempDir(), logger); err == nil {
		t.Fatal("expected error for missing source")
	}
}

// ── RunBatch ──────────────────────────────────────────────────────────────────

func TestTotalSize(t *testing.T) {
	modtime := time.Now()
	tasks := []Task{
		{Src: scan.FileInfo{Size: 100, ModTime: modtime}},
		{Src: scan.FileInfo{Size: 250, ModTime: modtime}},
		{Src: scan.FileInfo{Size: 50, ModTime: modtime}},
	}
	if got := TotalSize(tasks); got != 400 {
		t.Errorf("TotalSize = %d, want 400", got)
	}
}

func TestTotalSize_Empty(t *testing.T) {
	if got := TotalSize(nil); got != 0 {
		t.Errorf("TotalSize(nil) = %d, want 0", got)
	}
}

func TestRunBatch_AllSucceed(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	logger := log.New(io.Discard, "", 0)
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)

	for _, name := range []string{"A.NEF", "B.NEF", "C.JPG"} {
		p := filepath.Join(src, name)
		os.WriteFile(p, []byte("data"), 0644)
		os.Chtimes(p, modtime, modtime)
	}

	tasks := []Task{
		{Src: scan.FileInfo{AbsPath: filepath.Join(src, "A.NEF"), RelPath: "A.NEF", Size: 4, ModTime: modtime}, DstRelPath: "photos/2026-03-25/A.NEF"},
		{Src: scan.FileInfo{AbsPath: filepath.Join(src, "B.NEF"), RelPath: "B.NEF", Size: 4, ModTime: modtime}, DstRelPath: "photos/2026-03-25/B.NEF"},
		{Src: scan.FileInfo{AbsPath: filepath.Join(src, "C.JPG"), RelPath: "C.JPG", Size: 4, ModTime: modtime}, DstRelPath: "photos/2026-03-25/C.JPG"},
	}

	if errs := RunBatch(tasks, dst, logger); errs != 0 {
		t.Errorf("errCount = %d, want 0", errs)
	}
}

func TestRunBatch_CountsErrors(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	logger := log.New(io.Discard, "", 0)
	modtime := time.Now()

	okFile := filepath.Join(src, "ok.NEF")
	os.WriteFile(okFile, []byte("data"), 0644)

	tasks := []Task{
		{Src: scan.FileInfo{AbsPath: okFile, RelPath: "ok.NEF", Size: 4, ModTime: modtime}, DstRelPath: "photos/2026-03-25/ok.NEF"},
		{Src: scan.FileInfo{AbsPath: "/nonexistent.NEF", RelPath: "missing.NEF", Size: 100, ModTime: modtime}, DstRelPath: "photos/2026-03-25/missing.NEF"},
	}

	if errs := RunBatch(tasks, dst, logger); errs != 1 {
		t.Errorf("errCount = %d, want 1", errs)
	}
}

func TestCopyAndVerify_CollisionLogged(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)

	content := []byte("new camera file")
	srcFile := filepath.Join(src, "DSC_0001.NEF")
	os.WriteFile(srcFile, content, 0644)
	os.Chtimes(srcFile, modtime, modtime)

	// Pre-existing file at destination — forces a collision.
	dstDir := filepath.Join(dst, "photos/2026-03-25")
	os.MkdirAll(dstDir, 0755)
	os.WriteFile(filepath.Join(dstDir, "DSC_0001.NEF"), []byte("existing"), 0644)

	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", 0)

	task := Task{
		Src:        scan.FileInfo{AbsPath: srcFile, RelPath: "DCIM/DSC_0001.NEF", Size: int64(len(content)), ModTime: modtime},
		DstRelPath: "photos/2026-03-25/DSC_0001.NEF",
	}
	if err := CopyAndVerify(task, dst, logger); err != nil {
		t.Fatalf("CopyAndVerify: %v", err)
	}

	entry := logBuf.String()
	if !strings.Contains(entry, "COLLISION") {
		t.Error("expected COLLISION entry in log")
	}
	if !strings.Contains(entry, "original=photos/2026-03-25/DSC_0001.NEF") {
		t.Errorf("expected original path in log, got: %s", entry)
	}
	if !strings.Contains(entry, "saved=") {
		t.Error("expected saved path in log")
	}
}

func TestRunBatch_ContinuesAfterError(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	logger := log.New(io.Discard, "", 0)
	modtime := time.Now()

	lastFile := filepath.Join(src, "last.NEF")
	os.WriteFile(lastFile, []byte("data"), 0644)

	tasks := []Task{
		{Src: scan.FileInfo{AbsPath: "/nonexistent.NEF", RelPath: "bad.NEF", Size: 100, ModTime: modtime}, DstRelPath: "photos/2026-03-25/bad.NEF"},
		{Src: scan.FileInfo{AbsPath: lastFile, RelPath: "last.NEF", Size: 4, ModTime: modtime}, DstRelPath: "photos/2026-03-25/last.NEF"},
	}

	RunBatch(tasks, dst, logger)

	// last.NEF should have been copied despite the earlier error.
	if _, err := os.Stat(filepath.Join(dst, "photos/2026-03-25/last.NEF")); err != nil {
		t.Error("batch stopped after error — last file was not copied")
	}
}
