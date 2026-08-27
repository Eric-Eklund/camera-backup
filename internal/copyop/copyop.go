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
	"sync/atomic"
	"time"

	"github.com/Eric-Eklund/lumen/internal/checksum"
	"github.com/Eric-Eklund/lumen/internal/scan"
	"github.com/Eric-Eklund/lumen/internal/ui"
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

// setup opens source and destination and returns the terminal progress writer
// plus the writer the copy should report bytes to — the same thing unless an
// observer is watching, in which case both are fed.
// On error the destination file is cleaned up by the caller.
func setup(t Task, extra io.Writer) (src, dst *os.File, dstPath string, pw *ui.ProgressWriter, w io.Writer, err error) {
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
	w = pw
	if extra != nil {
		w = io.MultiWriter(pw, extra)
	}
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
// Used wherever the destination is the only copy of the file: Camera→SSD, and
// the direct source→NAS dump that bypasses the SSD.
// Source is opened read-only. On failure the partial destination file is removed.
// Modtime is preserved so downstream date-based comparisons remain correct.
// A writeTimeout > 0 bounds how long the write may take — pass it for NAS
// destinations, where a hung mount would otherwise block forever, and 0 for
// local disks; <= 0 disables it.
func CopyAndVerify(t Task, logger *log.Logger, writeTimeout time.Duration) error {
	return copyVerified(t, logger, writeTimeout, nil)
}

// copyVerified is CopyAndVerify with an optional second writer receiving the
// same byte counts as the terminal progress bar.
func copyVerified(t Task, logger *log.Logger, writeTimeout time.Duration, extra io.Writer) error {
	intendedPath := filepath.Join(t.DstRoot, t.DstRelPath)
	src, dst, dstPath, pw, w, err := setup(t, extra)
	if err != nil {
		return err
	}
	// On timeout the stalled goroutine may still be reading src; this deferred
	// Close makes its next read fail so it winds down instead of copying on.
	defer src.Close()

	err = copyStream(dst, src, w, t.Src.RelPath, dstPath, true, writeTimeout, func() { os.Remove(dstPath) })
	pw.Done()
	if errors.Is(err, errWriteTimeout) {
		return nasTimeoutError(writeTimeout, t.Src.RelPath, dstPath)
	}
	if err != nil {
		os.Remove(dstPath)
		return err
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

// errWriteTimeout reports that a destination stopped accepting data for longer
// than the configured timeout — the signature of a hung network mount.
var errWriteTimeout = errors.New("destination write stalled")

// destFile is the subset of *os.File that copyStream writes through. It is an
// interface rather than *os.File so tests can substitute a writer that hangs
// on demand, standing in for a dropped network mount.
type destFile interface {
	io.WriteCloser
	Sync() error
}

// copyStream streams src into dst (mirroring progress into pw) and closes dst,
// flushing to disk first when syncBeforeClose is set (the verified path).
//
// timeout bounds how long the destination may go without accepting a single
// byte — it is a stall detector, not a deadline for the file. That distinction
// is the whole point: a 40 GB video over a phone hotspot takes far longer than
// any timeout anyone would set against a hung mount, and measuring the total
// transfer instead would abort every large file on a perfectly healthy link.
// A timeout <= 0 waits forever.
//
// When the destination does stall, copyStream returns errWriteTimeout
// immediately; the stuck transfer is left running in the background and
// onAbandoned is called once it finally returns, so the caller can clean up the
// destination without blocking on the hung mount. On any other error dst is
// closed before returning.
func copyStream(dst destFile, src io.Reader, pw io.Writer, relPath, dstPath string, syncBeforeClose bool, timeout time.Duration, onAbandoned func()) error {
	// The progress sink is silenced on timeout: an abandoned transfer must not
	// touch it after the batch has moved on (it may be a closed events channel
	// or a progress line another file is now rendering to). It doubles as the
	// stall detector's clock — every chunk the destination accepts passes
	// through it, so "last write" and "still making progress" are the same
	// fact observed in one place.
	sw := &stopWriter{w: pw}
	sw.mark()
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, copyBufSize)
		if _, err := io.CopyBuffer(io.MultiWriter(dst, sw), src, buf); err != nil {
			dst.Close()
			done <- fmt.Errorf("copying %q: %w", relPath, err)
			return
		}
		if syncBeforeClose {
			if err := dst.Sync(); err != nil {
				dst.Close()
				done <- fmt.Errorf("sync %q: %w", dstPath, err)
				return
			}
		}
		// Without Sync, Close is the only place deferred write failures (common
		// on NAS mounts) surface — never report OK past it.
		if err := dst.Close(); err != nil {
			done <- fmt.Errorf("close %q: %w", dstPath, err)
			return
		}
		done <- nil
	}()

	if timeout <= 0 {
		return <-done
	}
	// Poll rather than arm a single timer: the deadline moves forward with
	// every chunk the destination accepts, and a timer that had to be reset
	// per chunk would cost more than the check does.
	ticker := time.NewTicker(stallCheckInterval(timeout))
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			if sw.idleFor() < timeout {
				continue // still making progress
			}
			sw.stopped.Store(true)
			go func() {
				<-done
				onAbandoned()
			}()
			return errWriteTimeout
		}
	}
}

// stallCheckInterval is how often a copy is asked whether it has moved. A
// quarter of the timeout keeps the overshoot proportional, bounded so a long
// timeout does not go unchecked for minutes and a short one (tests, an
// impatient config) is still noticed promptly.
func stallCheckInterval(timeout time.Duration) time.Duration {
	d := timeout / 4
	if d < 10*time.Millisecond {
		return 10 * time.Millisecond
	}
	if d > time.Second {
		return time.Second
	}
	return d
}

// stopWriter forwards writes until stopped is set, then swallows them, and
// records when the last one came through so the caller can tell a slow
// destination from a stopped one.
type stopWriter struct {
	w       io.Writer
	stopped atomic.Bool
	lastAt  atomic.Int64 // UnixNano of the last write to reach the destination
}

func (s *stopWriter) Write(p []byte) (int, error) {
	if s.stopped.Load() {
		return len(p), nil
	}
	// The destination is written first by the MultiWriter above, so reaching
	// this line means those bytes have already been accepted.
	s.mark()
	return s.w.Write(p)
}

// mark records that the destination has just accepted data.
func (s *stopWriter) mark() { s.lastAt.Store(time.Now().UnixNano()) }

// idleFor reports how long the destination has gone without accepting a byte.
func (s *stopWriter) idleFor() time.Duration {
	return time.Since(time.Unix(0, s.lastAt.Load()))
}

// nasTimeoutError builds the user-facing error for a stalled NAS write.
// Removing the partial file inline would block on the same hung mount, so
// copyStream removes it in the background once the stalled write returns — or,
// if this process exits first, on the next run that can reach the share.
func nasTimeoutError(writeTimeout time.Duration, relPath, dstPath string) error {
	return fmt.Errorf("NAS write on %q made no progress for %v — check your mount options (consider soft,timeo=... for NFS); the partial file %q is removed once the mount recovers",
		relPath, writeTimeout, dstPath)
}

// Copy copies one task to dstRoot quickly without sync or SHA256 verification.
// Used for SSD→NAS where speed matters; the verify command checks integrity separately.
// Source is opened read-only. On failure the partial destination file is removed.
// Modtime is preserved so downstream date-based comparisons remain correct.
// A writeTimeout > 0 bounds how long each file may take, so a hung network
// mount fails the file instead of blocking the whole batch; <= 0 disables it.
func Copy(t Task, logger *log.Logger, writeTimeout time.Duration) error {
	return copyFast(t, logger, writeTimeout, nil)
}

// copyFast is Copy with an optional second writer receiving the same byte
// counts as the terminal progress bar.
func copyFast(t Task, logger *log.Logger, writeTimeout time.Duration, extra io.Writer) error {
	intendedPath := filepath.Join(t.DstRoot, t.DstRelPath)
	src, dst, dstPath, pw, w, err := setup(t, extra)
	if err != nil {
		return err
	}
	// On timeout the stalled goroutine may still be reading src; this deferred
	// Close makes its next read fail so it winds down instead of copying on.
	defer src.Close()

	err = copyStream(dst, src, w, t.Src.RelPath, dstPath, false, writeTimeout, func() { os.Remove(dstPath) })
	pw.Done()
	if errors.Is(err, errWriteTimeout) {
		return nasTimeoutError(writeTimeout, t.Src.RelPath, dstPath)
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
// Sends go through guard: a copy abandoned after a write timeout can outlive
// the batch, and a bare send would race the events channel being closed.
type progressWriter struct {
	relPath string
	size    int64
	written int64
	events  chan<- FileProgress
	guard   *sendGuard
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	pw.guard.send(pw.events, FileProgress{RelPath: pw.relPath, Written: pw.written, Size: pw.size})
	return n, nil
}

// sendGuard serialises event sends against the closing of the events channel,
// so late sends from abandoned copies become no-ops instead of panics.
type sendGuard struct {
	mu     sync.RWMutex
	closed bool
}

func (g *sendGuard) send(events chan<- FileProgress, fp FileProgress) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		return
	}
	events <- fp
}

func (g *sendGuard) closeCh(events chan<- FileProgress) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	close(events)
}

// copyWithWriter copies a single task to its destination root, writing
// progress bytes to w. If doVerify is true the destination is flushed to disk
// and SHA256-checked against the source. On failure the partial destination
// file is removed. writeTimeout bounds the write on either path, so a direct
// (verified) dump to the NAS is protected from a hung mount too; pass 0 for
// local destinations.
func copyWithWriter(t Task, doVerify bool, logger *log.Logger, w io.Writer, writeTimeout time.Duration) error {
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

	if err := copyStream(dst, src, w, t.Src.RelPath, dstPath, doVerify, writeTimeout, func() { os.Remove(dstPath) }); err != nil {
		if errors.Is(err, errWriteTimeout) {
			return nasTimeoutError(writeTimeout, t.Src.RelPath, dstPath)
		}
		os.Remove(dstPath)
		return err
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
// nasWriteTimeout bounds each copy to a NAS root so a hung mount fails the
// file instead of pinning a worker forever; pass 0 for local destinations.
func RunBatchParallel(ctx context.Context, tasks []Task, logger *log.Logger, doVerify bool, nasWriteTimeout time.Duration, workers int, events chan<- FileProgress) int {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	errCount := 0
	guard := &sendGuard{}

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

			pw := &progressWriter{relPath: t.DstRelPath, size: t.Src.Size, events: events, guard: guard}
			err := copyWithWriter(t, doVerify, logger, pw, nasWriteTimeout)
			guard.send(events, FileProgress{RelPath: t.DstRelPath, Written: t.Src.Size, Size: t.Src.Size, Done: true, Err: err})
			if err != nil {
				logger.Printf("ERROR  %v", err)
				mu.Lock()
				errCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	guard.closeCh(events)
	return errCount
}

// RunBatch copies a slice of tasks using CopyAndVerify if verify is true,
// else the faster Copy. Errors are logged and counted; the batch continues on failure.
// Returns the number of files that failed.
// nasWriteTimeout bounds each copy to a NAS root so a hung mount fails the
// file instead of the batch — including verified direct dumps; pass 0 for
// local destinations such as Camera→SSD.
func RunBatch(tasks []Task, logger *log.Logger, verify bool, nasWriteTimeout time.Duration) int {
	return RunBatchObserved(tasks, logger, verify, nasWriteTimeout, nil)
}

// RunBatchObserved is RunBatch with a sink for the progress the terminal bars
// already show, so something outside this process can follow along. observe is
// called as bytes land and once more when each file finishes; it must not
// block, since the copy waits on it. A nil observe makes this exactly
// RunBatch.
func RunBatchObserved(tasks []Task, logger *log.Logger, verify bool, nasWriteTimeout time.Duration, observe func(FileProgress)) int {
	errCount := 0
	for i, t := range tasks {
		fmt.Printf("\n  [%d/%d] %s\n", i+1, len(tasks), t.DstRelPath)

		var extra io.Writer
		if observe != nil {
			extra = &observeWriter{observe: observe, relPath: t.Src.RelPath, size: t.Src.Size}
		}

		var err error
		if verify {
			err = copyVerified(t, logger, nasWriteTimeout, extra)
		} else {
			err = copyFast(t, logger, nasWriteTimeout, extra)
		}
		if err != nil {
			ui.Red.Printf("  ERROR: %v\n", err)
			logger.Printf("ERROR  %v", err)
			errCount++
		}
		if observe != nil {
			observe(FileProgress{RelPath: t.Src.RelPath, Written: t.Src.Size, Size: t.Src.Size, Done: true, Err: err})
		}
	}
	return errCount
}

// observeWriter turns the bytes handed to the progress bar into FileProgress
// snapshots. It writes nothing anywhere — it only counts.
type observeWriter struct {
	observe func(FileProgress)
	relPath string
	size    int64
	written int64
}

func (w *observeWriter) Write(p []byte) (int, error) {
	w.written += int64(len(p))
	w.observe(FileProgress{RelPath: w.relPath, Written: w.written, Size: w.size})
	return len(p), nil
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
