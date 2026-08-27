// White-box tests — same package so we can reach unexported safeCreate.
package copyop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eric-Eklund/lumen/internal/scan"
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

// ── Copy ─────────────────────────────────────────────────────────────

func TestCopy_Success(t *testing.T) {
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
		DstRoot:    dst,
		DstRelPath: "2026-03-25/DSC_0001.NEF",
	}

	// A generous write timeout must not affect a healthy copy.
	if err := Copy(task, logger, time.Minute); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	dstFile := filepath.Join(dst, "2026-03-25/DSC_0001.NEF")

	got, _ := os.ReadFile(dstFile)
	if string(got) != string(content) {
		t.Error("destination content does not match source")
	}

	fi, _ := os.Stat(dstFile)
	if fi.ModTime().Unix() != modtime.Unix() {
		t.Errorf("modtime = %v, want %v", fi.ModTime(), modtime)
	}
}

func TestCopy_NeverOverwrites(t *testing.T) {
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
	dstDir := filepath.Join(dst, "2026-03-25")
	os.MkdirAll(dstDir, 0755)
	existing := filepath.Join(dstDir, "DSC_0001.NEF")
	os.WriteFile(existing, []byte("existing file"), 0644)

	task := Task{
		Src:        scan.FileInfo{AbsPath: srcFile, RelPath: "DCIM/DSC_0001.NEF", Size: int64(len(content)), ModTime: modtime},
		DstRoot:    dst,
		DstRelPath: "2026-03-25/DSC_0001.NEF",
	}

	if err := Copy(task, logger, 0); err != nil {
		t.Fatalf("Copy: %v", err)
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

func TestCopy_MissingSource(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	task := Task{
		Src:        scan.FileInfo{AbsPath: "/nonexistent/DSC_0001.NEF", RelPath: "DSC_0001.NEF", Size: 100, ModTime: time.Now()},
		DstRoot:    t.TempDir(),
		DstRelPath: "2026-03-25/DSC_0001.NEF",
	}
	if err := Copy(task, logger, 0); err == nil {
		t.Fatal("expected error for missing source")
	}
}

// ── CopyAndVerify ─────────────────────────────────────────────────────────────

// A direct source→NAS dump copies with verification *and* a write timeout —
// the timeout must not disturb a healthy copy.
func TestCopyAndVerify_WithWriteTimeout(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	content := []byte("verified straight to the NAS")
	srcFile := filepath.Join(src, "DSC_0001.NEF")
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	os.WriteFile(srcFile, content, 0644)
	os.Chtimes(srcFile, modtime, modtime)

	task := Task{
		Src:        scan.FileInfo{AbsPath: srcFile, RelPath: "DCIM/DSC_0001.NEF", Size: int64(len(content)), ModTime: modtime},
		DstRoot:    dst,
		DstRelPath: "2026/2026-03/2026-03-25/DSC_0001.NEF",
	}
	if err := CopyAndVerify(task, logger, time.Minute); err != nil {
		t.Fatalf("CopyAndVerify: %v", err)
	}

	dstFile := filepath.Join(dst, "2026/2026-03/2026-03-25/DSC_0001.NEF")
	got, _ := os.ReadFile(dstFile)
	if string(got) != string(content) {
		t.Error("destination content does not match source")
	}
	fi, err := os.Stat(dstFile)
	if err != nil {
		t.Fatalf("stat destination: %v", err)
	}
	if fi.ModTime().Unix() != modtime.Unix() {
		t.Errorf("modtime = %v, want %v", fi.ModTime(), modtime)
	}
}

func TestCopyAndVerify_MissingSource(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	task := Task{
		Src:        scan.FileInfo{AbsPath: "/nonexistent/DSC_0001.NEF", RelPath: "DSC_0001.NEF", Size: 100, ModTime: time.Now()},
		DstRoot:    t.TempDir(),
		DstRelPath: "2026-03-25/DSC_0001.NEF",
	}
	if err := CopyAndVerify(task, logger, time.Minute); err == nil {
		t.Fatal("expected error for missing source")
	}
}

// ── copyStream write timeout ──────────────────────────────────────────────────

// blockingWriteCloser blocks every Write until release is closed — a stand-in
// for a hard-mounted NAS share whose connection has dropped.
type blockingWriteCloser struct{ release chan struct{} }

func (b *blockingWriteCloser) Write(p []byte) (int, error) { <-b.release; return len(p), nil }
func (b *blockingWriteCloser) Sync() error                 { return nil }
func (b *blockingWriteCloser) Close() error                { return nil }

func TestCopyStream_HangingWriterTimesOut(t *testing.T) {
	w := &blockingWriteCloser{release: make(chan struct{})}
	abandoned := make(chan struct{})

	err := copyStream(w, strings.NewReader("stuck data"), io.Discard,
		"VIDEO001.MOV", "/nas/VIDEO001.MOV", false, 50*time.Millisecond, func() { close(abandoned) })
	if !errors.Is(err, errWriteTimeout) {
		t.Fatalf("err = %v, want errWriteTimeout", err)
	}

	// Cleanup must not run while the write is still stalled — it would block
	// on the hung mount just like the write itself.
	select {
	case <-abandoned:
		t.Fatal("onAbandoned ran while the write was still stalled")
	default:
	}

	// Once the stalled write finally returns, cleanup must fire.
	close(w.release)
	select {
	case <-abandoned:
	case <-time.After(5 * time.Second):
		t.Fatal("onAbandoned was not called after the stalled write returned")
	}
}

// A verified copy to the NAS (the direct dump path) must time out on a hung
// mount too — the verify flag used to disable the timeout entirely.
func TestCopyStream_HangingSyncedWriteTimesOut(t *testing.T) {
	w := &blockingWriteCloser{release: make(chan struct{})}
	abandoned := make(chan struct{})

	err := copyStream(w, strings.NewReader("stuck verified data"), io.Discard,
		"DSC_0001.NEF", "/nas/DSC_0001.NEF", true, 50*time.Millisecond, func() { close(abandoned) })
	if !errors.Is(err, errWriteTimeout) {
		t.Fatalf("err = %v, want errWriteTimeout", err)
	}

	close(w.release)
	select {
	case <-abandoned:
	case <-time.After(5 * time.Second):
		t.Fatal("onAbandoned was not called after the stalled write returned")
	}
}

// A synced copy must still report a failing Sync rather than claiming success.
func TestCopyStream_SyncErrorFails(t *testing.T) {
	w := &syncFailWriteCloser{}
	err := copyStream(w, strings.NewReader("data"), io.Discard,
		"DSC_0001.NEF", "/nas/DSC_0001.NEF", true, 0,
		func() { t.Error("onAbandoned called for a copy that did not time out") })
	if err == nil || !strings.Contains(err.Error(), "sync") {
		t.Fatalf("err = %v, want a sync error", err)
	}
	if !w.closed {
		t.Error("destination was not closed after the sync error")
	}
}

// syncFailWriteCloser accepts writes but fails to flush, like a full or
// disconnected destination filesystem.
type syncFailWriteCloser struct{ closed bool }

func (s *syncFailWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (s *syncFailWriteCloser) Sync() error                 { return errors.New("no space left on device") }
func (s *syncFailWriteCloser) Close() error                { s.closed = true; return nil }

// slowWriteCloser delays each Write but always completes.
type slowWriteCloser struct{ delay time.Duration }

func (s *slowWriteCloser) Write(p []byte) (int, error) { time.Sleep(s.delay); return len(p), nil }
func (s *slowWriteCloser) Sync() error                 { return nil }
func (s *slowWriteCloser) Close() error                { return nil }

func TestCopyStream_SlowWriterWithinTimeout(t *testing.T) {
	w := &slowWriteCloser{delay: time.Millisecond}
	err := copyStream(w, strings.NewReader("slow but steady"), io.Discard,
		"DSC_0001.NEF", "/nas/DSC_0001.NEF", false, 5*time.Second,
		func() { t.Error("onAbandoned called for a copy that finished in time") })
	if err != nil {
		t.Fatalf("copyStream: %v", err)
	}
}

func TestCopyStream_NoTimeoutWaitsForever(t *testing.T) {
	w := &slowWriteCloser{delay: time.Millisecond}
	err := copyStream(w, strings.NewReader("no deadline"), io.Discard,
		"DSC_0001.NEF", "/nas/DSC_0001.NEF", false, 0,
		func() { t.Error("onAbandoned called with timeout disabled") })
	if err != nil {
		t.Fatalf("copyStream: %v", err)
	}
}

func TestSendGuard_LateSendAfterCloseIsNoOp(t *testing.T) {
	g := &sendGuard{}
	events := make(chan FileProgress, 1)
	g.closeCh(events)
	// A copy abandoned after a write timeout may fire long after the batch
	// closed the channel — the send must be swallowed, not panic.
	g.send(events, FileProgress{RelPath: "late.MOV"})
	if _, ok := <-events; ok {
		t.Error("unexpected event on closed channel")
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

func TestSortBySizeAsc(t *testing.T) {
	modtime := time.Now()
	tasks := []Task{
		{Src: scan.FileInfo{RelPath: "big.MOV", Size: 3_000_000, ModTime: modtime}},
		{Src: scan.FileInfo{RelPath: "small.JPG", Size: 200, ModTime: modtime}},
		{Src: scan.FileInfo{RelPath: "mid.NEF", Size: 45_000, ModTime: modtime}},
	}

	SortBySizeAsc(tasks)

	want := []string{"small.JPG", "mid.NEF", "big.MOV"}
	for i, w := range want {
		if tasks[i].Src.RelPath != w {
			t.Errorf("tasks[%d] = %q, want %q", i, tasks[i].Src.RelPath, w)
		}
	}
}

func TestSortBySizeAsc_StableForEqualSizes(t *testing.T) {
	modtime := time.Now()
	tasks := []Task{
		{Src: scan.FileInfo{RelPath: "a.JPG", Size: 100, ModTime: modtime}},
		{Src: scan.FileInfo{RelPath: "b.JPG", Size: 100, ModTime: modtime}},
		{Src: scan.FileInfo{RelPath: "c.JPG", Size: 100, ModTime: modtime}},
	}

	SortBySizeAsc(tasks)

	want := []string{"a.JPG", "b.JPG", "c.JPG"}
	for i, w := range want {
		if tasks[i].Src.RelPath != w {
			t.Errorf("tasks[%d] = %q, want %q — equal sizes must keep their order", i, tasks[i].Src.RelPath, w)
		}
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
		{Src: scan.FileInfo{AbsPath: filepath.Join(src, "A.NEF"), RelPath: "A.NEF", Size: 4, ModTime: modtime}, DstRoot: dst, DstRelPath: "2026-03-25/A.NEF"},
		{Src: scan.FileInfo{AbsPath: filepath.Join(src, "B.NEF"), RelPath: "B.NEF", Size: 4, ModTime: modtime}, DstRoot: dst, DstRelPath: "2026-03-25/B.NEF"},
		{Src: scan.FileInfo{AbsPath: filepath.Join(src, "C.JPG"), RelPath: "C.JPG", Size: 4, ModTime: modtime}, DstRoot: dst, DstRelPath: "2026-03-25/C.JPG"},
	}

	if errs := RunBatch(tasks, logger, false, 0); errs != 0 {
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
		{Src: scan.FileInfo{AbsPath: okFile, RelPath: "ok.NEF", Size: 4, ModTime: modtime}, DstRoot: dst, DstRelPath: "2026-03-25/ok.NEF"},
		{Src: scan.FileInfo{AbsPath: "/nonexistent.NEF", RelPath: "missing.NEF", Size: 100, ModTime: modtime}, DstRoot: dst, DstRelPath: "2026-03-25/missing.NEF"},
	}

	if errs := RunBatch(tasks, logger, false, 0); errs != 1 {
		t.Errorf("errCount = %d, want 1", errs)
	}
}

func TestCopy_CollisionLogged(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)

	content := []byte("new camera file")
	srcFile := filepath.Join(src, "DSC_0001.NEF")
	os.WriteFile(srcFile, content, 0644)
	os.Chtimes(srcFile, modtime, modtime)

	// Pre-existing file at destination — forces a collision.
	dstDir := filepath.Join(dst, "2026-03-25")
	os.MkdirAll(dstDir, 0755)
	os.WriteFile(filepath.Join(dstDir, "DSC_0001.NEF"), []byte("existing"), 0644)

	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", 0)

	task := Task{
		Src:        scan.FileInfo{AbsPath: srcFile, RelPath: "DCIM/DSC_0001.NEF", Size: int64(len(content)), ModTime: modtime},
		DstRoot:    dst,
		DstRelPath: "2026-03-25/DSC_0001.NEF",
	}
	if err := Copy(task, logger, 0); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	entry := logBuf.String()
	if !strings.Contains(entry, "COLLISION") {
		t.Error("expected COLLISION entry in log")
	}
	if !strings.Contains(entry, "original=2026-03-25/DSC_0001.NEF") {
		t.Errorf("expected original path in log, got: %s", entry)
	}
	if !strings.Contains(entry, "saved=") {
		t.Error("expected saved path in log")
	}
}

// ── RunBatchParallel ──────────────────────────────────────────────────────────

// makeTasks writes n small source files and returns copy tasks targeting dst.
func makeTasks(t *testing.T, src, dst string, n int) []Task {
	t.Helper()
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	tasks := make([]Task, 0, n)
	for i := 0; i < n; i++ {
		name := filepath.Join(src, filepath.Base(t.Name())+string(rune('A'+i))+".NEF")
		os.WriteFile(name, []byte("data"), 0644)
		os.Chtimes(name, modtime, modtime)
		tasks = append(tasks, Task{
			Src:        scan.FileInfo{AbsPath: name, RelPath: filepath.Base(name), Size: 4, ModTime: modtime},
			DstRoot:    dst,
			DstRelPath: "2026-03-25/" + filepath.Base(name),
		})
	}
	return tasks
}

// drain consumes all progress events and returns how many files completed.
func drain(events <-chan FileProgress) (done int) {
	for fp := range events {
		if fp.Done && fp.Err == nil {
			done++
		}
	}
	return done
}

func TestRunBatchParallel_AllSucceed(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	logger := log.New(io.Discard, "", 0)
	tasks := makeTasks(t, src, dst, 3)

	events := make(chan FileProgress, 64)
	doneCh := make(chan int, 1)
	go func() { doneCh <- drain(events) }()

	// A generous NAS write timeout must not affect healthy copies.
	failures := RunBatchParallel(context.Background(), tasks, logger, false, time.Minute, 2, events)
	if failures != 0 {
		t.Errorf("failures = %d, want 0", failures)
	}
	if done := <-doneCh; done != 3 {
		t.Errorf("completed files = %d, want 3", done)
	}
}

func TestRunBatchParallel_CancelledBeforeStart(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	logger := log.New(io.Discard, "", 0)
	tasks := makeTasks(t, src, dst, 5)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the batch starts

	events := make(chan FileProgress, 64)
	doneCh := make(chan int, 1)
	go func() { doneCh <- drain(events) }()

	failures := RunBatchParallel(ctx, tasks, logger, false, 0, 2, events)
	if failures != 0 {
		t.Errorf("failures = %d, want 0 — cancelled tasks must not count as failures", failures)
	}
	if done := <-doneCh; done != 0 {
		t.Errorf("completed files = %d, want 0 — no task should start after cancel", done)
	}
	// events must be closed (drain returned), and no destination files created.
	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Errorf("destination has %d entries, want 0", len(entries))
	}
}

func TestRunBatchParallel_CancelMidBatch(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	logger := log.New(io.Discard, "", 0)
	tasks := makeTasks(t, src, dst, 20)

	ctx, cancel := context.WithCancel(context.Background())
	// Unbuffered: every event send blocks until received, so cancel() is
	// guaranteed to land before later files start — keeps the test deterministic.
	events := make(chan FileProgress)
	doneCh := make(chan int, 1)
	go func() {
		n := 0
		for fp := range events {
			if fp.Done && fp.Err == nil {
				n++
				if n == 2 {
					cancel() // cancel once a couple of files have finished
				}
			}
		}
		doneCh <- n
	}()

	failures := RunBatchParallel(ctx, tasks, logger, false, 0, 1, events)
	done := <-doneCh

	if failures != 0 {
		t.Errorf("failures = %d, want 0", failures)
	}
	if done >= len(tasks) {
		t.Errorf("all %d files copied despite cancellation", done)
	}
	if done < 2 {
		t.Errorf("completed files = %d, want at least the 2 that finished before cancel", done)
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
		{Src: scan.FileInfo{AbsPath: "/nonexistent.NEF", RelPath: "bad.NEF", Size: 100, ModTime: modtime}, DstRoot: dst, DstRelPath: "2026-03-25/bad.NEF"},
		{Src: scan.FileInfo{AbsPath: lastFile, RelPath: "last.NEF", Size: 4, ModTime: modtime}, DstRoot: dst, DstRelPath: "2026-03-25/last.NEF"},
	}

	RunBatch(tasks, logger, false, 0)

	// last.NEF should have been copied despite the earlier error.
	if _, err := os.Stat(filepath.Join(dst, "2026-03-25/last.NEF")); err != nil {
		t.Error("batch stopped after error — last file was not copied")
	}
}
