package tui

import (
	"fmt"
	"image"
	"log"
	"path/filepath"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/copyop"
	"github.com/Eric-Eklund/camera-backup/internal/preview"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/status"
	"github.com/Eric-Eklund/camera-backup/internal/ui"
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

// preparePhase2Cmd rescans SSD vs NAS after Phase 1 so Phase 2 always copies
// from the SSD (never the camera) and includes files just copied in Phase 1.
func preparePhase2Cmd(cfg *config.Config, logger *log.Logger) tea.Cmd {
	return func() tea.Msg {
		tasks, err := nasSyncTasks(cfg)
		if err != nil {
			logger.Printf("ERROR phase 2 scan: %v", err)
			return phase2ReadyMsg{err: err}
		}
		return phase2ReadyMsg{tasks: tasks}
	}
}

// nasSyncTasks scans SSD and NAS fresh and returns the SSD→NAS copy tasks,
// videos first (so large files are prioritised if the connection drops).
func nasSyncTasks(cfg *config.Config) ([]copyop.Task, error) {
	exts := cfg.NormalisedExtensions()
	ssdFiles, err := scan.Walk(cfg.SSD, exts)
	if err != nil {
		return nil, err
	}
	nasFiles, _ := scan.Walk(cfg.NAS, exts)
	nasIndex := scan.IndexByRelPath(nasFiles)
	missing := scan.MissingByRelPath(ssdFiles, nasIndex)

	tasks := make([]copyop.Task, 0, len(missing))
	for _, f := range missing {
		tasks = append(tasks, copyop.Task{Src: f, DstRelPath: f.RelPath})
	}
	sortVideosFirst(tasks, cfg)
	return tasks, nil
}

func sortVideosFirst(tasks []copyop.Task, cfg *config.Config) {
	sort.SliceStable(tasks, func(i, j int) bool {
		return cfg.Category(tasks[i].Src.RelPath) == "videos" &&
			cfg.Category(tasks[j].Src.RelPath) != "videos"
	})
}

// checkSpace returns an error if dst lacks free space for all tasks.
// If free space cannot be determined the copy is allowed to proceed.
func checkSpace(dst string, tasks []copyop.Task) error {
	needed := copyop.TotalSize(tasks)
	free, err := ui.FreeSpace(dst)
	if err != nil {
		return nil
	}
	if needed > free {
		return fmt.Errorf("not enough space on %s: need %s but only %s free",
			dst, ui.FormatBytes(needed), ui.FormatBytes(free))
	}
	return nil
}

// verifyCmd runs the verify pass, streaming per-file results to the model
// via p.Send (which is safe to call from any goroutine).
func verifyCmd(cfg *config.Config, logger *log.Logger, p *tea.Program) tea.Cmd {
	return func() tea.Msg {
		bad := 0
		total := 0
		err := verify.RunWithCallback(cfg, logger, func(done, tot int, r verify.FileResult) {
			total = tot
			if len(r.Issues) > 0 {
				bad++
			}
			p.Send(verifyFileMsg{done: done, total: tot, result: r})
		})
		if err != nil {
			logger.Printf("ERROR verify: %v", err)
			return verifyDoneMsg{bad: -1, total: total}
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

func fullImageCmd(absPath string) tea.Cmd {
	return func() tea.Msg {
		img, err := preview.FullImage(absPath)
		return fullImageMsg{file: absPath, img: img, err: err}
	}
}

// kittyDrawCmd draws img via the Kitty Graphics Protocol shortly after the
// next bubbletea frame is flushed, so the image lands on top of the rendered
// placeholder area.
func kittyDrawCmd(img image.Image, cols, rows, row, col int) tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		preview.KittyClear()
		_ = preview.KittyRenderAtCell(img, cols, rows, row, col)
		return nil
	})
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
