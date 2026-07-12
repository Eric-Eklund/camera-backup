package copyop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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

// Task describes one file to copy: the source file, the destination root for
// its category (photos or videos may live in different directories), and the
// date-based relative path under that root.
type Task struct {
	Src        scan.FileInfo
	DstRoot    string // e.g. "/mnt/ssd/Photos"
	DstRelPath string // e.g. "2026/2026-03/2026-03-24/DSC_0001.NEF"
}

// setup opens source and destination and returns a progress writer.
// On error the destination file is cleaned up by the caller.
func setup(t Task) (src, dst *os.File, dstPath string, pw *ui.ProgressWriter, err error) {
	intendedPath := filepath.Join(t.DstRoot, t.DstRelPath)
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

func logCollision(t Task, intendedPath, dstPath string, logger *log.Logger) {
	if dstPath != intendedPath {
		savedRel, _ := filepath.Rel(t.DstRoot, dstPath)
		ui.Yellow.Printf("\n  ⚠️  COLLISION: %s already existed — saved as %s\n", t.DstRelPath, savedRel)
		logger.Printf("COLLISION  original=%s  saved=%s", t.DstRelPath, savedRel)
	}
}

// CopyAndVerify copies one task to dstRoot, syncs to disk, then SHA256-verifies src vs dst.
// Used for Camera→SSD where the SSD is the source of truth.
// Source is opened read-only. On failure the partial destination file is removed.
// Modtime is preserved so downstream date-based comparisons remain correct.
func CopyAndVerify(t Task, logger *log.Logger) error {
	intendedPath := filepath.Join(t.DstRoot, t.DstRelPath)
	src, dst, dstPath, pw, err := setup(t)
	if err != nil {
		return err
	}
	defer src.Close()

	buf := make([]byte, copyBufSize)
	if _, err := io.CopyBuffer(io.MultiWriter(dst, pw), src, buf); err != nil {
		pw.Done()
		dst.Close()
		os.Remove(dstPath)
		return fmt.Errorf("copying %q: %w", t.Src.RelPath, err)
	}
	pw.Done()

	if err := dst.Sync(); err != nil {
		dst.Close()
		os.Remove(dstPath)
		return fmt.Errorf("sync %q: %w", dstPath, err)
	}
	// Close errors surface deferred write failures (common on network mounts) —
	// a copy is not OK until the file is safely closed.
	if err := dst.Close(); err != nil {
		os.Remove(dstPath)
		return fmt.Errorf("close %q: %w", dstPath, err)
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

	logCollision(t, intendedPath, dstPath, logger)
	return nil
}

// errWriteTimeout reports that a destination write did not finish within the
// configured timeout — the signature of a hung network mount.
var errWriteTimeout = errors.New("destination write timed out")

// copyStream streams src into dst (mirroring progress into pw) and closes dst.
// A timeout <= 0 waits forever. If the transfer is still running when the
// timeout elapses, copyStream returns errWriteTimeout immediately; the stalled
// transfer is left running in the background and onAbandoned is called once it
// finally returns, so the caller can clean up the destination without blocking
// on the hung mount. On any other error dst is closed before returning.
func copyStream(dst io.WriteCloser, src io.Reader, pw io.Writer, relPath, dstPath string, timeout time.Duration, onAbandoned func()) error {
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, copyBufSize)
		if _, err := io.CopyBuffer(io.MultiWriter(dst, pw), src, buf); err != nil {
			dst.Close()
			done <- fmt.Errorf("copying %q: %w", relPath, err)
			return
		}
		// This fast path skips Sync, so Close is the only place deferred write
		// failures (common on NAS mounts) surface — never report OK past it.
		if err := dst.Close(); err != nil {
			done <- fmt.Errorf("close %q: %w", dstPath, err)
			return
		}
		done <- nil
	}()

	if timeout <= 0 {
		return <-done
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		go func() {
			<-done
			onAbandoned()
		}()
		return errWriteTimeout
	}
}

// Copy copies one task to dstRoot quickly without sync or SHA256 verification.
// Used for SSD→NAS where speed matters; the verify command checks integrity separately.
// Source is opened read-only. On failure the partial destination file is removed.
// Modtime is preserved so downstream date-based comparisons remain correct.
// A writeTimeout > 0 bounds how long each file may take, so a hung network
// mount fails the file instead of blocking the whole batch; <= 0 disables it.
func Copy(t Task, logger *log.Logger, writeTimeout time.Duration) error {
	intendedPath := filepath.Join(t.DstRoot, t.DstRelPath)
	src, dst, dstPath, pw, err := setup(t)
	if err != nil {
		return err
	}
	// On timeout the stalled goroutine may still be reading src; this deferred
	// Close makes its next read fail so it winds down instead of copying on.
	defer src.Close()

	err = copyStream(dst, src, pw, t.Src.RelPath, dstPath, writeTimeout, func() { os.Remove(dstPath) })
	pw.Done()
	if errors.Is(err, errWriteTimeout) {
		// Removing the partial file now would block on the same hung mount —
		// copyStream removes it in the background once the write returns.
		return fmt.Errorf("NAS write timed out after %v on %q — check your mount options (consider soft,timeo=... for NFS); partial file %q is removed when the mount recovers",
			writeTimeout, t.Src.RelPath, dstPath)
	}
	if err != nil {
		os.Remove(dstPath)
		return err
	}

	_ = os.Chtimes(dstPath, t.Src.ModTime, t.Src.ModTime)

	ui.Green.Println("  ✅")
	logger.Printf("COPY OK  %s", dstPath)

	logCollision(t, intendedPath, dstPath, logger)
	return nil
}

// SortBySizeAsc orders tasks by source file size, smallest first (stable),
// so the files most likely to complete over a flaky connection go first.
func SortBySizeAsc(tasks []Task) {
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Src.Size < tasks[j].Src.Size })
}

// TotalSize returns the sum of source file sizes across all tasks.
func TotalSize(tasks []Task) int64 {
	var n int64
	for _, t := range tasks {
		n += t.Src.Size
	}
	return n
}

// FileProgress is a progress snapshot sent by RunBatchParallel for each active file.
type FileProgress struct {
	RelPath string
	Written int64
	Size    int64
	Done    bool
	Err     error
}

// progressWriter is an io.Writer that sends FileProgress events to a channel.
type progressWriter struct {
	relPath string
	size    int64
	written int64
	events  chan<- FileProgress
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	pw.events <- FileProgress{RelPath: pw.relPath, Written: pw.written, Size: pw.size}
	return n, nil
}

// copyWithWriter copies a single task to its destination root, writing
// progress bytes to w. If doVerify is true the destination is SHA256-checked
// against the source. On failure the partial destination file is removed.
func copyWithWriter(t Task, doVerify bool, logger *log.Logger, w io.Writer) error {
	intendedPath := filepath.Join(t.DstRoot, t.DstRelPath)
	if err := os.MkdirAll(filepath.Dir(intendedPath), 0755); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(intendedPath), err)
	}
	src, err := os.OpenFile(t.Src.AbsPath, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open source %q: %w", t.Src.AbsPath, err)
	}
	defer src.Close()

	dst, dstPath, err := safeCreate(intendedPath)
	if err != nil {
		return fmt.Errorf("create dest %q: %w", intendedPath, err)
	}

	buf := make([]byte, copyBufSize)
	if _, err := io.CopyBuffer(io.MultiWriter(dst, w), src, buf); err != nil {
		dst.Close()
		os.Remove(dstPath)
		return fmt.Errorf("copying %q: %w", t.Src.RelPath, err)
	}

	if doVerify {
		if err := dst.Sync(); err != nil {
			dst.Close()
			os.Remove(dstPath)
			return fmt.Errorf("sync %q: %w", dstPath, err)
		}
	}
	// Close errors surface deferred write failures (common on network mounts) —
	// a copy is not OK until the file is safely closed.
	if err := dst.Close(); err != nil {
		os.Remove(dstPath)
		return fmt.Errorf("close %q: %w", dstPath, err)
	}

	if doVerify {
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
		logger.Printf("COPY OK (verified)  %-50s  sha256=%s", dstPath, dstHash)
	} else {
		logger.Printf("COPY OK  %s", dstPath)
	}

	_ = os.Chtimes(dstPath, t.Src.ModTime, t.Src.ModTime)

	if dstPath != intendedPath {
		savedRel, _ := filepath.Rel(t.DstRoot, dstPath)
		logger.Printf("COLLISION  original=%s  saved=%s", t.DstRelPath, savedRel)
	}
	return nil
}

// RunBatchParallel runs up to workers concurrent copies, sending FileProgress
// snapshots to events as each chunk completes. Closes events when all tasks finish.
// Returns the number of files that failed.
//
// Cancelling ctx stops gracefully: files already being copied run to
// completion (so the destination never holds partial files), but queued
// tasks are not started. Cancelled tasks are not counted as failures.
func RunBatchParallel(ctx context.Context, tasks []Task, logger *log.Logger, doVerify bool, workers int, events chan<- FileProgress) int {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	errCount := 0

loop:
	for _, t := range tasks {
		if ctx.Err() != nil {
			logger.Printf("CANCELLED  batch stopped before %s", t.DstRelPath)
			break
		}
		t := t
		wg.Add(1)
		// Acquire a worker slot, but bail out if the batch is cancelled while
		// waiting so no new file starts after cancellation.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			logger.Printf("CANCELLED  batch stopped before %s", t.DstRelPath)
			break loop
		}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			pw := &progressWriter{relPath: t.DstRelPath, size: t.Src.Size, events: events}
			err := copyWithWriter(t, doVerify, logger, pw)
			events <- FileProgress{RelPath: t.DstRelPath, Written: t.Src.Size, Size: t.Src.Size, Done: true, Err: err}
			if err != nil {
				logger.Printf("ERROR  %v", err)
				mu.Lock()
				errCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	close(events)
	return errCount
}

// RunBatch copies a slice of tasks using CopyAndVerify if verify is true,
// else the faster Copy. Errors are logged and counted; the batch continues on failure.
// Returns the number of files that failed.
// nasWriteTimeout bounds each fast (non-verify, SSD→NAS) copy so a hung mount
// fails the file instead of the batch; it never applies to the verified
// Camera→SSD path, and <= 0 disables it.
func RunBatch(tasks []Task, logger *log.Logger, verify bool, nasWriteTimeout time.Duration) int {
	copyFn := func(t Task, logger *log.Logger) error { return Copy(t, logger, nasWriteTimeout) }
	if verify {
		copyFn = CopyAndVerify
	}
	errCount := 0
	for i, t := range tasks {
		fmt.Printf("\n  [%d/%d] %s\n", i+1, len(tasks), t.DstRelPath)
		if err := copyFn(t, logger); err != nil {
			ui.Red.Printf("  ERROR: %v\n", err)
			logger.Printf("ERROR  %v", err)
			errCount++
		}
	}
	return errCount
}

// CheckSpace returns an error if any destination filesystem lacks free space
// for its share of the tasks. Roots that resolve to the same filesystem are
// checked against their combined size so a shared disk is not double-counted.
// Roots whose free space cannot be determined are allowed to proceed.
func CheckSpace(tasks []Task) error {
	needPerRoot := map[string]int64{}
	for _, t := range tasks {
		needPerRoot[t.DstRoot] += t.Src.Size
	}

	type group struct {
		need  int64
		free  int64
		roots []string
	}
	groups := map[string]*group{}
	for root, need := range needPerRoot {
		// The root itself may not exist yet (created on first copy) — fall
		// back to its parent for space information.
		free, err := ui.FreeSpace(root)
		if err != nil {
			root = filepath.Dir(root)
			if free, err = ui.FreeSpace(root); err != nil {
				continue
			}
		}
		key := root // fall back to per-root checking
		if id, err := ui.FilesystemID(root); err == nil {
			key = fmt.Sprintf("fs:%d", id)
		}
		g := groups[key]
		if g == nil {
			g = &group{free: free}
			groups[key] = g
		}
		g.need += need
		g.roots = append(g.roots, root)
	}

	for _, g := range groups {
		if g.need > g.free {
			return fmt.Errorf("not enough space on %s: need %s but only %s free",
				strings.Join(g.roots, ", "), ui.FormatBytes(g.need), ui.FormatBytes(g.free))
		}
	}
	return nil
}
