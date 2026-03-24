package copyop

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/Eric-Eklund/camera-backup/internal/checksum"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/ui"
)

// Task describes one file to copy: the source file and where it ends up on the destination.
type Task struct {
	Src        scan.FileInfo
	DstRelPath string // e.g. "photos/2026-03-24/DSC_0001.NEF"
}

// CopyAndVerify copies one task to dstRoot, shows inline progress, then SHA256-verifies.
// Source is opened read-only. On failure the partial destination file is removed.
// Modtime is preserved so downstream date-based comparisons remain correct.
func CopyAndVerify(t Task, dstRoot string, logger *log.Logger) error {
	dstPath := filepath.Join(dstRoot, t.DstRelPath)

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(dstPath), err)
	}

	src, err := os.OpenFile(t.Src.AbsPath, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open source %q: %w", t.Src.AbsPath, err)
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create dest %q: %w", dstPath, err)
	}
	defer dst.Close()

	label := filepath.Base(t.Src.RelPath)
	pw := ui.NewProgressWriter(label, t.Src.Size, os.Stdout)
	if _, err := io.Copy(io.MultiWriter(dst, pw), src); err != nil {
		pw.Done()
		os.Remove(dstPath)
		return fmt.Errorf("copying %q: %w", t.Src.RelPath, err)
	}
	pw.Done()

	if err := dst.Sync(); err != nil {
		return fmt.Errorf("sync %q: %w", dstPath, err)
	}

	// Preserve source modtime so date-based comparisons stay correct.
	_ = os.Chtimes(dstPath, t.Src.ModTime, t.Src.ModTime)

	// SHA256 verify.
	fmt.Printf("    Verifying %-28s ", label)
	srcHash, err := checksum.File(t.Src.AbsPath)
	if err != nil {
		return fmt.Errorf("checksum source %q: %w", t.Src.RelPath, err)
	}
	dstHash, err := checksum.File(dstPath)
	if err != nil {
		return fmt.Errorf("checksum dest %q: %w", t.DstRelPath, err)
	}
	if srcHash != dstHash {
		os.Remove(dstPath)
		return fmt.Errorf("checksum mismatch %q: src=%s… dst=%s…", t.Src.RelPath, srcHash[:8], dstHash[:8])
	}
	ui.Green.Println("✅")
	logger.Printf("COPY OK  %-50s  sha256=%s", t.DstRelPath, dstHash)
	return nil
}

// RunBatch copies a slice of tasks to dstRoot.
// Errors are logged and counted; the batch continues on failure.
// Returns the number of files that failed.
func RunBatch(tasks []Task, dstRoot string, logger *log.Logger) int {
	errCount := 0
	for i, t := range tasks {
		fmt.Printf("\n  [%d/%d] %s\n", i+1, len(tasks), t.DstRelPath)
		if err := CopyAndVerify(t, dstRoot, logger); err != nil {
			ui.Red.Printf("  ERROR: %v\n", err)
			logger.Printf("ERROR  %v", err)
			errCount++
		}
	}
	return errCount
}
