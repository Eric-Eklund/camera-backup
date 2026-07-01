package tui

import (
	"image"
	"log"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/copyop"
	"github.com/Eric-Eklund/camera-backup/internal/preview"
	"github.com/Eric-Eklund/camera-backup/internal/status"
	"github.com/Eric-Eklund/camera-backup/internal/verify"
)

func statusScanCmd(cfg *config.Config, logger *log.Logger) tea.Cmd {
	return func() tea.Msg {
		r, err := status.Compute(cfg, logger)
		return statusReadyMsg{result: r, err: err}
	}
}

func copyPhase1Cmd(tasks []copyop.Task, dstRoot string, logger *log.Logger, workers int, events chan<- copyop.FileProgress) tea.Cmd {
	return func() tea.Msg {
		failures := copyop.RunBatchParallel(tasks, dstRoot, logger, true, workers, events)
		return phase1DoneMsg{failures: failures}
	}
}

func copyPhase2Cmd(tasks []copyop.Task, dstRoot string, logger *log.Logger, workers int, events chan<- copyop.FileProgress) tea.Cmd {
	return func() tea.Msg {
		failures := copyop.RunBatchParallel(tasks, dstRoot, logger, false, workers, events)
		return copyDoneMsg{failures: failures}
	}
}

func syncCmd(tasks []copyop.Task, dstRoot string, logger *log.Logger, workers int, events chan<- copyop.FileProgress) tea.Cmd {
	return func() tea.Msg {
		failures := copyop.RunBatchParallel(tasks, dstRoot, logger, false, workers, events)
		return copyDoneMsg{failures: failures}
	}
}

func verifyCmd(cfg *config.Config, logger *log.Logger) tea.Cmd {
	return func() tea.Msg {
		bad := 0
		total := 0
		err := verify.RunWithCallback(cfg, logger, func(done, tot int, r verify.FileResult) {
			total = tot
			// The channel send happens inside the Model via the progressFn — but since
			// bubbletea tea.Cmd runs in a goroutine we can't send msgs directly.
			// Instead we accumulate and return the final counts.
			if len(r.Issues) > 0 {
				bad++
			}
		})
		if err != nil {
			bad = -1 // sentinel for error
		}
		return verifyDoneMsg{bad: bad, total: total}
	}
}

func thumbnailCmd(absPath string) tea.Cmd {
	return func() tea.Msg {
		img, err := preview.Thumbnail(absPath)
		return thumbnailMsg{file: absPath, img: img, err: err}
	}
}

// drainProgressCmd reads FileProgress events from events and sends them as tea.Msgs.
// Returns when events is closed.
func drainProgressCmd(events <-chan copyop.FileProgress, p *tea.Program) tea.Cmd {
	return func() tea.Msg {
		for fp := range events {
			p.Send(fileProgressMsg{p: fp})
		}
		return nil
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// watchDevicesCmd starts an fsnotify watcher on the parent directories of the configured
// device paths. When a CREATE or REMOVE event is detected it sends a DeviceChangedMsg.
func watchDevicesCmd(cfg *config.Config, p *tea.Program) tea.Cmd {
	return func() tea.Msg {
		dirs := uniqueDirs(cfg.Source, cfg.SSD, cfg.NAS)
		if len(dirs) == 0 {
			return nil
		}
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			return nil
		}
		for _, d := range dirs {
			_ = watcher.Add(d)
		}
		go func() {
			defer watcher.Close()
			for {
				select {
				case ev, ok := <-watcher.Events:
					if !ok {
						return
					}
					if ev.Has(fsnotify.Create) || ev.Has(fsnotify.Remove) {
						p.Send(deviceChangedMsg{})
					}
				case _, ok := <-watcher.Errors:
					if !ok {
						return
					}
				}
			}
		}()
		return nil // watcher runs in background goroutine
	}
}

func uniqueDirs(paths ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		d := filepath.Dir(p)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// buildPhase1Tasks builds Camera→SSD copy tasks from a StatusResult.
func buildPhase1Tasks(r *status.StatusResult, cfg *config.Config) []copyop.Task {
	tasks := make([]copyop.Task, 0, len(r.MissingOnSSD))
	for _, f := range r.MissingOnSSD {
		cat := cfg.Category(f.RelPath)
		tasks = append(tasks, copyop.Task{
			Src:        f,
			DstRelPath: f.DestRelPath(cat),
		})
	}
	return tasks
}

// buildPhase2Tasks builds SSD→NAS copy tasks from a StatusResult.
func buildPhase2Tasks(r *status.StatusResult) []copyop.Task {
	tasks := make([]copyop.Task, 0, len(r.MissingOnNAS))
	for _, f := range r.MissingOnNAS {
		tasks = append(tasks, copyop.Task{
			Src:        f,
			DstRelPath: f.RelPath,
		})
	}
	return tasks
}

// noopImage is a blank placeholder used when no thumbnail is available.
var noopImage image.Image
