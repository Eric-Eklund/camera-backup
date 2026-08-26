// Package progress publishes what a running copy is doing to a file other
// programs can read.
//
// The progress bars a copy prints exist for whoever is watching the terminal.
// A status bar, a phone notification or a script waiting for the backup to
// finish cannot see them, and polling `status --json` during a copy means
// scanning the same devices a second time. This writes the state of the batch
// in progress instead — a single small document, replaced as the copy runs.
//
// It is a *state* file rather than an event log on purpose: a widget wants to
// know how far along the copy is, not to replay how it got there. Reading it
// is `cat` and a JSON parse, with no history to fold up.
package progress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/copyop"
)

// writeInterval is how often the file is rewritten while bytes are flowing.
// Every chunk would mean thousands of rewrites a second for no visible gain;
// a file finishing always writes immediately regardless.
const writeInterval = 250 * time.Millisecond

// State is the document written to disk. Field names are a contract, like the
// ones in status --json.
type State struct {
	// Running is false in the document written when the batch ends. A reader
	// that finds Running true should still check UpdatedAt: a killed process
	// leaves its last document behind, and nothing rewrites it.
	Running bool `json:"running"`
	// Phase names what is being copied, e.g. "camera→ssd".
	Phase string `json:"phase"`
	// PID is the process doing the copying, so a reader can tell a stale
	// document from a live one.
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	UpdatedAt string `json:"updated_at"`

	Files FileCounts `json:"files"`
	Bytes ByteCounts `json:"bytes"`
	// Current holds the files being written right now — more than one when
	// several workers are copying at once.
	Current []Current `json:"current"`

	// BytesPerSecond is measured over the whole run, not the last moment, so
	// it does not jump about between a small photograph and a large video.
	BytesPerSecond int64 `json:"bytes_per_second"`
	// ETASeconds is null until enough has been copied to estimate one, and
	// once the batch is done.
	ETASeconds *int64 `json:"eta_seconds"`
}

type FileCounts struct {
	Done   int `json:"done"`
	Failed int `json:"failed"`
	Total  int `json:"total"`
}

type ByteCounts struct {
	// Done is everything transferred so far, including the part of a file
	// still being written. A bar drawn from Done/Total therefore keeps moving
	// through a large video instead of standing still until it finishes —
	// which is the case a progress bar exists for.
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

type Current struct {
	File    string `json:"file"`
	Written int64  `json:"written"`
	Size    int64  `json:"size"`
}

// Writer keeps the state document current. Its methods are safe to call from
// several copy workers at once.
type Writer struct {
	path  string
	phase string
	// start is when the current batch began, so the speed describes the
	// transfer in progress rather than an average across a pause between
	// phases.
	start time.Time

	mu         sync.Mutex
	total      int
	totalBytes int64
	done       int
	failed     int
	doneBytes  int64
	inFlight   map[string]Current
	lastWrite  time.Time
	writeErr   error
}

// New starts publishing to path. The first document is written immediately, so
// a reader polling the file sees the copy start rather than waiting for the
// first chunk of a large video.
func New(path, phase string, totalFiles int, totalBytes int64) (*Writer, error) {
	w := &Writer{
		path:       path,
		phase:      phase,
		start:      time.Now(),
		total:      totalFiles,
		totalBytes: totalBytes,
		inFlight:   map[string]Current{},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("progress: %w", err)
	}
	if err := w.write(false); err != nil {
		return nil, err
	}
	return w, nil
}

// Observe takes one progress event from the copy. It never blocks on I/O for
// longer than a rewrite of a small file, and skips the rewrite entirely for
// events that arrive within writeInterval of the last one.
func (w *Writer) Observe(fp copyop.FileProgress) {
	w.mu.Lock()
	// A file entering or leaving the batch is worth a write whatever the
	// throttle says: those are the moments a reader is waiting for, and there
	// are only ever as many of them as there are files. Byte counts in between
	// are what the throttle is for.
	_, known := w.inFlight[fp.RelPath]
	force := fp.Done || !known
	if fp.Done {
		delete(w.inFlight, fp.RelPath)
		w.done++
		if fp.Err != nil {
			// The destination of a failed copy is removed, so its bytes are
			// not on the disk and must not be counted as transferred.
			w.failed++
		} else {
			w.doneBytes += fp.Size
		}
	} else {
		w.inFlight[fp.RelPath] = Current{File: fp.RelPath, Written: fp.Written, Size: fp.Size}
	}
	stale := time.Since(w.lastWrite) >= writeInterval
	w.mu.Unlock()

	if force || stale {
		_ = w.write(false)
	}
}

// StartBatch begins a new batch inside the same document: the counters reset
// and the phase changes, but running stays true.
//
// A copy is two batches — camera→SSD, then SSD→NAS — and closing the document
// between them would tell a reader the backup had finished while half of it
// was still to come.
func (w *Writer) StartBatch(phase string, totalFiles int, totalBytes int64) {
	w.mu.Lock()
	w.phase = phase
	w.total = totalFiles
	w.totalBytes = totalBytes
	w.done, w.failed, w.doneBytes = 0, 0, 0
	w.inFlight = map[string]Current{}
	w.start = time.Now()
	w.mu.Unlock()
	_ = w.write(false)
}

// Close writes the final document, marking the batch finished, and reports the
// first write error the run hit.
func (w *Writer) Close() error {
	w.mu.Lock()
	w.inFlight = map[string]Current{}
	w.mu.Unlock()
	if err := w.write(true); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeErr
}

// write renders the current state. final marks the batch as finished; the
// document goes out through a temporary file and a rename, so a reader either
// sees the previous document or the new one and never half of either.
func (w *Writer) write(final bool) error {
	w.mu.Lock()
	now := time.Now()
	st := State{
		Running:   !final,
		Phase:     w.phase,
		PID:       os.Getpid(),
		StartedAt: w.start.Format(time.RFC3339),
		UpdatedAt: now.Format(time.RFC3339),
		Files:     FileCounts{Done: w.done, Failed: w.failed, Total: w.total},
		Bytes:     ByteCounts{Done: w.doneBytes, Total: w.totalBytes},
	}
	// Always a list, never null: a reader should not have to handle two
	// spellings of "nothing is being written right now".
	st.Current = make([]Current, 0, len(w.inFlight))
	for _, c := range w.inFlight {
		st.Current = append(st.Current, c)
	}
	// Map iteration order is random; a document that reshuffles its own rows
	// between writes is needlessly hard to read.
	sort.Slice(st.Current, func(i, j int) bool { return st.Current[i].File < st.Current[j].File })

	// Bytes actually moved so far, including the part of each file in flight.
	moved := w.doneBytes
	for _, c := range st.Current {
		moved += c.Written
	}
	st.Bytes.Done = moved
	if elapsed := now.Sub(w.start).Seconds(); elapsed > 0 {
		st.BytesPerSecond = int64(float64(moved) / elapsed)
		if !final && st.BytesPerSecond > 0 && w.totalBytes > moved {
			eta := int64(float64(w.totalBytes-moved) / float64(st.BytesPerSecond))
			st.ETASeconds = &eta
		}
	}
	w.lastWrite = now
	w.mu.Unlock()

	err := writeAtomic(w.path, st)

	w.mu.Lock()
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	w.mu.Unlock()
	return err
}

func writeAtomic(path string, st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("progress: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".progress-*")
	if err != nil {
		return fmt.Errorf("progress: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("progress: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("progress: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("progress: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("progress: %w", err)
	}
	return nil
}
