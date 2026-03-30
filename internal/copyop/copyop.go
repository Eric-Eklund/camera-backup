package copyop

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Eric-Eklund/camera-backup/internal/checksum"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/ui"
)

const copyBufSize = 4 << 20 // 4 MB

// safeCreate opens a new file for writing, never overwriting an existing file.
// If dstPath already exists, it appends _1, _2, … before the extension until a
// free slot is found. Returns the open file and the final path used.
func safeCreate(dstPath string) (*os.File, string, error) {
	f, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err == nil {
		return f, dstPath, nil
	}
	if !os.IsExist(err) {
		return nil, "", err
	}
	ext := filepath.Ext(dstPath)
	stem := strings.TrimSuffix(dstPath, ext)
	for i := 1; i < 10000; i++ {
		candidate := fmt.Sprintf("%s_%d%s", stem, i, ext)
		f, err = os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
		if err == nil {
			return f, candidate, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("cannot find free filename for %q after 9999 attempts", dstPath)
}

// Task describes one file to copy: the source file and where it ends up on the destination.
type Task struct {
	Src        scan.FileInfo
	DstRelPath string // e.g. "photos/2026-03-24/DSC_0001.NEF"
}

// setup opens source and destination and returns a progress writer.
// On error the destination file is cleaned up by the caller.
func setup(t Task, dstRoot string) (src, dst *os.File, dstPath string, pw *ui.ProgressWriter, err error) {
	intendedPath := filepath.Join(dstRoot, t.DstRelPath)
	if err = os.MkdirAll(filepath.Dir(intendedPath), 0755); err != nil {
		err = fmt.Errorf("mkdir %q: %w", filepath.Dir(intendedPath), err)
		return
	}
	src, err = os.OpenFile(t.Src.AbsPath, os.O_RDONLY, 0)
	if err != nil {
		err = fmt.Errorf("open source %q: %w", t.Src.AbsPath, err)
		return
	}
	dst, dstPath, err = safeCreate(intendedPath)
	if err != nil {
		src.Close()
		src = nil
		err = fmt.Errorf("create dest %q: %w", intendedPath, err)
		return
	}
	pw = ui.NewProgressWriter(filepath.Base(dstPath), t.Src.Size, os.Stdout)
	return
}

func logCollision(t Task, dstRoot, intendedPath, dstPath string, logger *log.Logger) {
	if dstPath != intendedPath {
		savedRel, _ := filepath.Rel(dstRoot, dstPath)
		ui.Yellow.Printf("\n  ⚠️  COLLISION: %s already existed — saved as %s\n", t.DstRelPath, savedRel)
		logger.Printf("COLLISION  original=%s  saved=%s", t.DstRelPath, savedRel)
	}
}

// CopyAndVerify copies one task to dstRoot, syncs to disk, then SHA256-verifies src vs dst.
// Used for Camera→SSD where the SSD is the source of truth.
// Source is opened read-only. On failure the partial destination file is removed.
// Modtime is preserved so downstream date-based comparisons remain correct.
func CopyAndVerify(t Task, dstRoot string, logger *log.Logger) error {
	intendedPath := filepath.Join(dstRoot, t.DstRelPath)
	src, dst, dstPath, pw, err := setup(t, dstRoot)
	if err != nil {
		return err
	}
	defer src.Close()
	defer dst.Close()

	buf := make([]byte, copyBufSize)
	if _, err := io.CopyBuffer(io.MultiWriter(dst, pw), src, buf); err != nil {
		pw.Done()
		os.Remove(dstPath)
		return fmt.Errorf("copying %q: %w", t.Src.RelPath, err)
	}
	pw.Done()

	if err := dst.Sync(); err != nil {
		os.Remove(dstPath)
		return fmt.Errorf("sync %q: %w", dstPath, err)
	}

	_ = os.Chtimes(dstPath, t.Src.ModTime, t.Src.ModTime)

	label := filepath.Base(dstPath)
	fmt.Printf("    Verifying %-28s ", label)
	srcHash, err := checksum.File(t.Src.AbsPath)
	if err != nil {
		os.Remove(dstPath)
		return fmt.Errorf("checksum source %q: %w", t.Src.RelPath, err)
	}
	dstHash, err := checksum.File(dstPath)
	if err != nil {
		os.Remove(dstPath)
		return fmt.Errorf("checksum dest %q: %w", t.DstRelPath, err)
	}
	if srcHash != dstHash {
		os.Remove(dstPath)
		return fmt.Errorf("checksum mismatch %q: src=%s… dst=%s…", t.Src.RelPath, srcHash[:8], dstHash[:8])
	}
	ui.Green.Println("✅")
	logger.Printf("COPY OK (verified)  %-50s  sha256=%s", dstPath, dstHash)

	logCollision(t, dstRoot, intendedPath, dstPath, logger)
	return nil
}

// Copy copies one task to dstRoot quickly without sync or SHA256 verification.
// Used for SSD→NAS where speed matters; the verify command checks integrity separately.
// Source is opened read-only. On failure the partial destination file is removed.
// Modtime is preserved so downstream date-based comparisons remain correct.
func Copy(t Task, dstRoot string, logger *log.Logger) error {
	intendedPath := filepath.Join(dstRoot, t.DstRelPath)
	src, dst, dstPath, pw, err := setup(t, dstRoot)
	if err != nil {
		return err
	}
	defer src.Close()
	defer dst.Close()

	buf := make([]byte, copyBufSize)
	if _, err := io.CopyBuffer(io.MultiWriter(dst, pw), src, buf); err != nil {
		pw.Done()
		os.Remove(dstPath)
		return fmt.Errorf("copying %q: %w", t.Src.RelPath, err)
	}
	pw.Done()

	_ = os.Chtimes(dstPath, t.Src.ModTime, t.Src.ModTime)

	ui.Green.Println("  ✅")
	logger.Printf("COPY OK  %s", dstPath)

	logCollision(t, dstRoot, intendedPath, dstPath, logger)
	return nil
}

// TotalSize returns the sum of source file sizes across all tasks.
func TotalSize(tasks []Task) int64 {
	var n int64
	for _, t := range tasks {
		n += t.Src.Size
	}
	return n
}

// RunBatch copies a slice of tasks to dstRoot using CopyAndVerify if verify is true,
// else the faster Copy. Errors are logged and counted; the batch continues on failure.
// Returns the number of files that failed.
func RunBatch(tasks []Task, dstRoot string, logger *log.Logger, verify bool) int {
	copyFn := Copy
	if verify {
		copyFn = CopyAndVerify
	}
	errCount := 0
	for i, t := range tasks {
		fmt.Printf("\n  [%d/%d] %s\n", i+1, len(tasks), t.DstRelPath)
		if err := copyFn(t, dstRoot, logger); err != nil {
			ui.Red.Printf("  ERROR: %v\n", err)
			logger.Printf("ERROR  %v", err)
			errCount++
		}
	}
	return errCount
}
