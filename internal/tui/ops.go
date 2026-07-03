package tui

import (
	"context"
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

// runBatchCmd runs a parallel copy batch, streaming progress to events (closed
// by RunBatchParallel when done) and the failure count to result. The done
// message itself is emitted by drainProgressCmd — after every progress event
// has been forwarded — so completion counts are never racy.
func runBatchCmd(ctx context.Context, tasks []copyop.Task, dstRoot string, logger *log.Logger, doVerify bool, workers int, events chan<- copyop.FileProgress, result chan<- int) tea.Cmd {
	return func() tea.Msg {
		result <- copyop.RunBatchParallel(ctx, tasks, dstRoot, logger, doVerify, workers, events)
		return nil
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

// progressTickCmd re-renders the progress screen twice a second so speeds and
// ETA stay live even when no copy events arrive.
func progressTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return progressTickMsg{} })
}

// drainProgressCmd forwards FileProgress events as tea.Msgs until events is
// closed, then emits the batch-done message built from the failure count.
// Emitting it here guarantees it arrives after every progress event.
func drainProgressCmd(events <-chan copyop.FileProgress, result <-chan int, p *tea.Program, mkDone func(failures int) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		for fp := range events {
			p.Send(fileProgressMsg{p: fp})
		}
		return mkDone(<-result)
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
