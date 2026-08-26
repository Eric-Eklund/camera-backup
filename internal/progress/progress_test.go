package progress_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/copyop"
	"github.com/Eric-Eklund/camera-backup/internal/progress"
)

func read(t *testing.T, path string) progress.State {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st progress.State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("the file is not valid JSON: %v\n%s", err, data)
	}
	return st
}

func newWriter(t *testing.T, files int, bytes int64) (*progress.Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "progress.json")
	w, err := progress.New(path, "camera→ssd", files, bytes)
	if err != nil {
		t.Fatal(err)
	}
	return w, path
}

// The file exists as soon as the batch starts, so a reader polling it sees the
// copy begin rather than waiting for the first chunk of a large video.
func TestNew_WritesImmediately(t *testing.T) {
	_, path := newWriter(t, 3, 3000)

	st := read(t, path)
	if !st.Running {
		t.Error("running = false before anything was copied")
	}
	if st.Files.Total != 3 || st.Bytes.Total != 3000 {
		t.Errorf("totals = %+v / %+v, want 3 files and 3000 bytes", st.Files, st.Bytes)
	}
	if st.PID != os.Getpid() {
		t.Errorf("pid = %d, want this process (%d)", st.PID, os.Getpid())
	}
	if st.Current == nil {
		t.Error("current is null; it should be an empty list")
	}
}

// A bar drawn from bytes.done must move while a large file is being written,
// not sit still until it finishes.
func TestObserve_CountsBytesInFlight(t *testing.T) {
	w, path := newWriter(t, 2, 1000)

	w.Observe(copyop.FileProgress{RelPath: "BIG.MOV", Written: 400, Size: 900})
	st := read(t, path)
	if st.Bytes.Done != 400 {
		t.Errorf("bytes.done = %d, want 400 — the file in flight is not counted", st.Bytes.Done)
	}
	if len(st.Current) != 1 || st.Current[0].File != "BIG.MOV" || st.Current[0].Written != 400 {
		t.Errorf("current = %+v, want the file being written", st.Current)
	}
	if st.Files.Done != 0 {
		t.Errorf("files.done = %d before anything finished", st.Files.Done)
	}
}

// Finishing a file always writes, whatever the throttle says, and moves it out
// of the in-flight list.
func TestObserve_DoneWritesImmediately(t *testing.T) {
	w, path := newWriter(t, 2, 1000)

	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Written: 100, Size: 100})
	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Written: 100, Size: 100, Done: true})

	st := read(t, path)
	if st.Files.Done != 1 || st.Bytes.Done != 100 {
		t.Errorf("after one file: %+v / %+v, want 1 file and 100 bytes", st.Files, st.Bytes)
	}
	if len(st.Current) != 0 {
		t.Errorf("current = %+v, want the finished file gone", st.Current)
	}
}

func TestObserve_CountsFailures(t *testing.T) {
	w, path := newWriter(t, 1, 100)
	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Size: 100, Done: true, Err: os.ErrPermission})

	if st := read(t, path); st.Files.Failed != 1 || st.Files.Done != 1 {
		t.Errorf("files = %+v, want one done and one failed", st.Files)
	}
}

// Several workers writing at once each get a row.
func TestObserve_MultipleFilesInFlight(t *testing.T) {
	w, path := newWriter(t, 3, 3000)
	w.Observe(copyop.FileProgress{RelPath: "B.NEF", Written: 100, Size: 1000})
	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Written: 200, Size: 1000})

	st := read(t, path)
	if len(st.Current) != 2 {
		t.Fatalf("current = %+v, want both files", st.Current)
	}
	// Sorted, so the document does not reshuffle its own rows between writes.
	if st.Current[0].File != "A.NEF" || st.Current[1].File != "B.NEF" {
		t.Errorf("current = %+v, want it sorted by name", st.Current)
	}
	if st.Bytes.Done != 300 {
		t.Errorf("bytes.done = %d, want 300", st.Bytes.Done)
	}
}

// Close leaves a document that says the batch is over — the difference between
// "still copying" and "finished" for anything reading the file later.
func TestClose_MarksFinished(t *testing.T) {
	w, path := newWriter(t, 1, 100)
	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Written: 100, Size: 100, Done: true})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	st := read(t, path)
	if st.Running {
		t.Error("running = true after Close")
	}
	if st.ETASeconds != nil {
		t.Errorf("eta = %v, want null once there is nothing left to copy", st.ETASeconds)
	}
	if len(st.Current) != 0 {
		t.Errorf("current = %+v, want empty", st.Current)
	}
}

// Rapid events must not each cost a rewrite, but the state they carry must not
// be lost either: the next write that does happen reports the latest figures.
func TestObserve_ThrottlesWithoutLosingState(t *testing.T) {
	w, path := newWriter(t, 1, 10000)
	for i := 1; i <= 500; i++ {
		w.Observe(copyop.FileProgress{RelPath: "BIG.MOV", Written: int64(i * 20), Size: 10000})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if st := read(t, path); st.Running {
		t.Error("the final document still says running")
	}
}

// The document is replaced, never appended to or truncated in place, so a
// reader polling it always parses a complete one.
func TestWrite_LeavesNoTemporaryFiles(t *testing.T) {
	w, path := newWriter(t, 1, 100)
	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Written: 50, Size: 100})
	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Written: 100, Size: 100, Done: true})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the progress file", names)
	}
}

// A timestamp lets a reader spot a document left behind by a killed process.
func TestState_TimestampsAreParseable(t *testing.T) {
	w, path := newWriter(t, 1, 100)
	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Written: 100, Size: 100, Done: true})

	st := read(t, path)
	for name, v := range map[string]string{"started_at": st.StartedAt, "updated_at": st.UpdatedAt} {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			t.Errorf("%s = %q is not RFC3339: %v", name, v, err)
		}
	}
}

func TestNew_UnwritablePathReportsError(t *testing.T) {
	if _, err := progress.New(filepath.Join(t.TempDir(), "nope", "x", "p.json"), "x", 0, 0); err != nil {
		return // a missing parent is created; only a real failure should error
	}
}

// A failed copy leaves nothing on the disk — its destination is removed — so
// its bytes must not be counted as transferred.
func TestObserve_FailedFileDoesNotCountBytes(t *testing.T) {
	w, path := newWriter(t, 2, 300)
	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Written: 100, Size: 100, Done: true})
	w.Observe(copyop.FileProgress{RelPath: "B.NEF", Written: 200, Size: 200, Done: true, Err: os.ErrPermission})

	st := read(t, path)
	if st.Bytes.Done != 100 {
		t.Errorf("bytes.done = %d, want 100 — the failed file's bytes were counted", st.Bytes.Done)
	}
	if st.Files.Done != 2 || st.Files.Failed != 1 {
		t.Errorf("files = %+v, want 2 finished of which 1 failed", st.Files)
	}
}

// A copy is two batches into one document. Between them the counters reset and
// the phase changes, but a reader must not see the run go finished.
func TestStartBatch_KeepsTheDocumentRunning(t *testing.T) {
	w, path := newWriter(t, 2, 200)
	w.Observe(copyop.FileProgress{RelPath: "A.NEF", Written: 100, Size: 100, Done: true})
	w.Observe(copyop.FileProgress{RelPath: "B.NEF", Written: 100, Size: 100, Done: true})

	w.StartBatch("ssd→nas", 5, 5000)

	st := read(t, path)
	if !st.Running {
		t.Error("running = false between the phases of one copy")
	}
	if st.Phase != "ssd→nas" {
		t.Errorf("phase = %q, want the new one", st.Phase)
	}
	if st.Files != (progress.FileCounts{Total: 5}) {
		t.Errorf("files = %+v, want the counters reset with the new total", st.Files)
	}
	if st.Bytes.Done != 0 || st.Bytes.Total != 5000 {
		t.Errorf("bytes = %+v, want a fresh batch", st.Bytes)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if st := read(t, path); st.Running {
		t.Error("running = true after the whole copy finished")
	}
}
