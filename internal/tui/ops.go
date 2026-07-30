package tui

import (
	"context"
	"image"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/copyop"
	"github.com/Eric-Eklund/camera-backup/internal/preview"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/status"
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
func runBatchCmd(ctx context.Context, tasks []copyop.Task, logger *log.Logger, doVerify bool, writeTimeout time.Duration, workers int, events chan<- copyop.FileProgress, result chan<- int) tea.Cmd {
	return func() tea.Msg {
		result <- copyop.RunBatchParallel(ctx, tasks, logger, doVerify, writeTimeout, workers, events)
		return nil
	}
}

// preparePhase2Cmd rescans SSD vs NAS after Phase 1 so Phase 2 always copies
// from the SSD (never the camera) and includes files just copied in Phase 1.
func preparePhase2Cmd(cfg *config.Config, logger *log.Logger) tea.Cmd {
	return func() tea.Msg {
		tasks, skipped := nasSyncTasks(cfg)
		if skipped > 0 {
			logger.Printf("phase 2: %d files skipped — NAS category root unavailable", skipped)
		}
		return phase2ReadyMsg{tasks: tasks, skipped: skipped}
	}
}

// nasSyncTasks scans SSD and NAS fresh and returns the SSD→NAS copy tasks in
// the configured transfer order (videos first by default).
// Files whose NAS category root is unavailable are counted as skipped.
// Category is decided by extension, so a merged SSD tree can be split onto
// separate NAS roots and vice versa.
func nasSyncTasks(cfg *config.Config) (tasks []copyop.Task, skipped int) {
	exts := cfg.NormalisedExtensions()
	categoryFn := func(f scan.FileInfo) string { return cfg.Category(f.RelPath) }

	ssdPhotoFiles, ssdVideoFiles := scan.WalkDual(cfg.SSDPhotos, cfg.SSDVideos, exts)
	var ssdAll []scan.FileInfo
	if cfg.SSDMerged() {
		ssdAll = ssdPhotoFiles
	} else {
		ssdAll = append(append([]scan.FileInfo{}, ssdPhotoFiles...), ssdVideoFiles...)
	}
	photos, videos := scan.SplitByCategory(ssdAll, categoryFn)
	nasPhotoFiles, nasVideoFiles := scan.WalkDual(cfg.NASPhotos, cfg.NASVideos, exts)

	add := func(files []scan.FileInfo, cat string, nasFiles []scan.FileInfo) {
		missing := scan.MissingByRelPath(files, scan.IndexByRelPath(nasFiles))
		if len(missing) == 0 {
			return
		}
		nasRoot := cfg.NASRoot(cat)
		if !config.RootAvailable(nasRoot) {
			skipped += len(missing)
			return
		}
		seen := map[string]bool{}
		for _, f := range missing {
			key := strings.ToLower(f.RelPath)
			if seen[key] {
				continue
			}
			seen[key] = true
			tasks = append(tasks, copyop.Task{Src: f, DstRoot: nasRoot, DstRelPath: f.RelPath})
		}
	}
	add(videos, "videos", nasVideoFiles)
	add(photos, "photos", nasPhotoFiles)
	orderTasks(tasks, cfg)
	return tasks, skipped
}

// orderTasks applies the configured SSD→NAS transfer order: smallest files
// first when nas_sync_order = "size-asc" (most likely to complete on a flaky
// connection), otherwise videos first.
func orderTasks(tasks []copyop.Task, cfg *config.Config) {
	if cfg.SyncOrder() == config.OrderSizeAsc {
		copyop.SortBySizeAsc(tasks)
		return
	}
	sortVideosFirst(tasks, cfg)
}

func sortVideosFirst(tasks []copyop.Task, cfg *config.Config) {
	sort.SliceStable(tasks, func(i, j int) bool {
		return cfg.Category(tasks[i].Src.RelPath) == "videos" &&
			cfg.Category(tasks[j].Src.RelPath) != "videos"
	})
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
// The watcher runs until stop is closed, which is how a config change swaps it
// for one watching the new paths.
func watchDevicesCmd(cfg *config.Config, p *tea.Program, stop <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		// Every source candidate is watched, so swapping a card for an external
		// drive triggers a rescan whichever one the user plugs in.
		watched := append(cfg.SourceCandidates(), cfg.SSDPhotos, cfg.SSDVideos, cfg.NASPhotos, cfg.NASVideos)
		dirs := uniqueDirs(watched...)
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
				case <-stop:
					return
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

// buildPhase1Tasks builds Camera→SSD copy tasks for files whose category
// root is mounted. Files routed to an unavailable root are counted as skipped.
func buildPhase1Tasks(missing []scan.FileInfo, cfg *config.Config, r *status.StatusResult) (tasks []copyop.Task, skipped int) {
	for _, f := range missing {
		cat := cfg.Category(f.RelPath)
		if !r.SSDRootAvail(cat) {
			skipped++
			continue
		}
		tasks = append(tasks, copyop.Task{
			Src:        f,
			DstRoot:    cfg.SSDRoot(cat),
			DstRelPath: f.DestRelPath(),
		})
	}
	return tasks, skipped
}

// buildDirectTasks builds source→NAS copy tasks for a direct dump: the same
// date-based destination paths a Camera→SSD copy would produce, written
// straight to the NAS category roots. Files routed to an unavailable NAS root
// are counted as skipped.
func buildDirectTasks(missing []scan.FileInfo, cfg *config.Config, r *status.StatusResult) (tasks []copyop.Task, skipped int) {
	for _, f := range missing {
		cat := cfg.Category(f.RelPath)
		if !r.NASRootAvail(cat) {
			skipped++
			continue
		}
		tasks = append(tasks, copyop.Task{
			Src:        f,
			DstRoot:    cfg.NASRoot(cat),
			DstRelPath: f.DestRelPath(),
		})
	}
	orderTasks(tasks, cfg)
	return tasks, skipped
}

// buildSyncTasks builds SSD→NAS copy tasks from already-computed missing
// files, honouring the current selection and NAS root availability.
func buildSyncTasks(missing []scan.FileInfo, cfg *config.Config, r *status.StatusResult) (tasks []copyop.Task, skipped int) {
	seen := map[string]bool{}
	for _, f := range missing {
		cat := cfg.Category(f.RelPath)
		if !r.NASRootAvail(cat) {
			skipped++
			continue
		}
		key := cat + "|" + strings.ToLower(f.RelPath)
		if seen[key] {
			continue
		}
		seen[key] = true
		tasks = append(tasks, copyop.Task{Src: f, DstRoot: cfg.NASRoot(cat), DstRelPath: f.RelPath})
	}
	orderTasks(tasks, cfg)
	return tasks, skipped
}
