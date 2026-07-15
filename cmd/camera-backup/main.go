package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/copyop"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/status"
	"github.com/Eric-Eklund/camera-backup/internal/tui"
	"github.com/Eric-Eklund/camera-backup/internal/ui"
	"github.com/Eric-Eklund/camera-backup/internal/verify"
)

func main() {
	var configPath string

	root := &cobra.Command{
		Use:   "camera-backup",
		Short: "Incremental camera backup with SHA256 verification",
		Long: `Safely back up camera media from memory cards to a local SSD
and a remote NAS — incrementally and with SHA256 verification.

Typical workflow:
  1. camera-backup status      — see what needs copying
  2. camera-backup copy        — copy camera→SSD, pause, then SSD→NAS
  3. camera-backup status      — final check before formatting cards in-camera`,
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "Path to config.toml (default: next to binary)")

	// Resolve config path before any subcommand runs.
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if configPath == "" {
			p, err := config.DefaultConfigPath()
			if err != nil {
				return err
			}
			configPath = p
		}
		return nil
	}

	root.AddCommand(
		newStatusCmd(&configPath),
		newCopyCmd(&configPath),
		newSyncCmd(&configPath),
		newVerifyCmd(&configPath),
		newTUICmd(&configPath),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// initLogger creates a timestamped log file in logs/ next to the binary,
// falling back to ~/.local/state/camera-backup/logs when the binary's
// directory is not writable (e.g. installed in /usr/local/bin).
func initLogger() (*log.Logger, func(), error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	stamp := time.Now().Format("2006-01-02_15-04-05")
	f, logPath, err := createLogFile(filepath.Join(filepath.Dir(exe), "logs"), stamp)
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, nil, err
		}
		stateDir := filepath.Join(home, ".local", "state", "camera-backup", "logs")
		if f, logPath, err = createLogFile(stateDir, stamp); err != nil {
			return nil, nil, err
		}
	}
	logger := log.New(f, "", log.LstdFlags)
	logger.Printf("camera-backup started — log: %s", logPath)
	return logger, func() { f.Close() }, nil
}

func createLogFile(dir, stamp string) (*os.File, string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, "", err
	}
	logPath := filepath.Join(dir, stamp+".log")
	f, err := os.Create(logPath)
	if err != nil {
		return nil, "", err
	}
	return f, logPath, nil
}

func mustLoadConfig(configPath string) (*config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("cannot load config %q: %w\n\nCreate a config.toml next to the binary or pass --config.", configPath, err)
	}
	return cfg, nil
}

func newStatusCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show device availability and file sync status",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, cleanup, err := initLogger()
			if err != nil {
				return err
			}
			defer cleanup()

			cfg, err := mustLoadConfig(*configPath)
			if err != nil {
				return err
			}
			return status.Run(cfg, logger)
		},
	}
}

func newCopyCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "copy",
		Short: "Copy missing files camera→SSD, then (optionally) SSD→NAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, cleanup, err := initLogger()
			if err != nil {
				return err
			}
			defer cleanup()

			cfg, err := mustLoadConfig(*configPath)
			if err != nil {
				return err
			}
			return runCopy(cfg, logger)
		},
	}
}

// syncOptions controls which categories runSync transfers and in what order.
type syncOptions struct {
	videosOnly bool
	photosOnly bool
	order      string // config.OrderVideosFirst or config.OrderSizeAsc
}

func newSyncCmd(configPath *string) *cobra.Command {
	var opts syncOptions
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Copy missing files SSD→NAS (no camera required)",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, cleanup, err := initLogger()
			if err != nil {
				return err
			}
			defer cleanup()

			cfg, err := mustLoadConfig(*configPath)
			if err != nil {
				return err
			}
			if opts.order == "" {
				opts.order = cfg.SyncOrder()
			}
			if opts.order != config.OrderVideosFirst && opts.order != config.OrderSizeAsc {
				return fmt.Errorf("invalid --order %q (valid: %s, %s)", opts.order, config.OrderVideosFirst, config.OrderSizeAsc)
			}
			return runSync(cfg, logger, opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.videosOnly, "videos-only", "v", false, "Only sync video files to NAS")
	cmd.Flags().BoolVarP(&opts.photosOnly, "photos-only", "p", false, "Only sync photo files to NAS")
	cmd.Flags().StringVar(&opts.order, "order", "",
		"Transfer order: videos-first or size-asc (smallest files first, best on flaky connections; default from nas_sync_order, else videos-first)")
	cmd.MarkFlagsMutuallyExclusive("videos-only", "photos-only")
	return cmd
}

func newVerifyCmd(configPath *string) *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "SHA256 verify all files across camera, SSD, and NAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, cleanup, err := initLogger()
			if err != nil {
				return err
			}
			defer cleanup()

			cfg, err := mustLoadConfig(*configPath)
			if err != nil {
				return err
			}
			return verify.Run(cfg, logger, verbose)
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Print every file, not just failures")
	return cmd
}

func runCopy(cfg *config.Config, logger *log.Logger) error {
	exts := cfg.NormalisedExtensions()
	categoryFn := func(f scan.FileInfo) string { return cfg.Category(f.RelPath) }

	sourceAvail := isDir(cfg.Source)
	ssdPhotosAvail := config.RootAvailable(cfg.SSDPhotos)
	ssdVideosAvail := config.RootAvailable(cfg.SSDVideos)

	// ── Phase 1: Camera → SSD ─────────────────────────────────────────────────
	ui.Bold.Println("\n  Phase 1: Camera → SSD")
	fmt.Println("  ─────────────────────────────────────────")

	phase1Ran := false
	if !sourceAvail {
		ui.Yellow.Printf("  Camera not available at %s — skipping.\n", cfg.Source)
		ui.Yellow.Println("  To sync SSD → NAS only, run: camera-backup sync")
		logger.Println("Phase 1 skipped: camera not available")
	} else if !ssdPhotosAvail && !ssdVideosAvail {
		return fmt.Errorf("SSD not accessible at %s or %s", cfg.SSDPhotos, cfg.SSDVideos)
	} else {
		phase1Ran = true
		cameraFiles, err := scan.Walk(cfg.Source, exts)
		if err != nil {
			return err
		}
		cameraFiles, unstable := scan.SplitStable(cameraFiles, time.Now(), scan.StableAge)
		if len(unstable) > 0 {
			ui.Yellow.Printf("  ⚠️  %d file(s) skipped — modified moments ago, possibly still being written. Re-run when the card is idle.\n", len(unstable))
			logger.Printf("Phase 1: %d file(s) skipped — modtime within %s of scan", len(unstable), scan.StableAge)
		}
		camPhotos, camVideos := scan.SplitByCategory(cameraFiles, categoryFn)
		ssdPhotoFiles, ssdVideoFiles := scan.WalkDual(cfg.SSDPhotos, cfg.SSDVideos, exts)

		// Build tasks per category; a category whose root is unavailable is
		// skipped with a warning so the other category can still be backed up.
		var tasks []copyop.Task
		addCategory := func(files []scan.FileInfo, cat string, avail bool, dstFiles []scan.FileInfo) {
			if len(files) == 0 {
				return
			}
			if !avail {
				ui.Yellow.Printf("  ⚠️  %d %s file(s) skipped — %s root not available at %s\n",
					len(files), cat, cat, cfg.SSDRoot(cat))
				logger.Printf("Phase 1: %d %s files skipped, root unavailable", len(files), cat)
				return
			}
			for _, f := range scan.MissingFromDest(files, scan.IndexByRelPath(dstFiles)) {
				tasks = append(tasks, copyop.Task{Src: f, DstRoot: cfg.SSDRoot(cat), DstRelPath: f.DestRelPath()})
			}
		}
		addCategory(camPhotos, "photos", ssdPhotosAvail, ssdPhotoFiles)
		addCategory(camVideos, "videos", ssdVideosAvail, ssdVideoFiles)

		if len(tasks) == 0 {
			ui.Green.Println("\n  SSD is already up to date — nothing to copy.")
			logger.Println("SSD already up to date")
		} else {
			if err := copyop.CheckSpace(tasks); err != nil {
				return err
			}
			ui.Bold.Printf("\n  Copying %d file(s) to SSD...\n", len(tasks))
			errs := copyop.RunBatch(tasks, logger, true, 0)
			fmt.Println()
			if errs > 0 {
				ui.Red.Printf("  ❌  %d file(s) failed to copy — do not disconnect the camera.\n", errs)
				ui.Red.Println("  Check the log, fix the issue, and re-run.")
				return fmt.Errorf("%d file(s) failed during Camera → SSD", errs)
			}
			ui.Green.Printf("  ✅  %d file(s) copied and verified.\n", len(tasks))
		}
	}

	// ── Pause ─────────────────────────────────────────────────────────────────
	ui.PrintSeparator()
	if phase1Ran {
		ui.Bold.Println("  Camera backup to SSD is complete.")
		fmt.Println("  You may now disconnect and power off the camera.")
		fmt.Println()
	}
	if !ui.AskYesNo("  Continue to sync SSD → NAS? [y/n]: ") {
		logger.Println("Phase 2 skipped: user declined")
		return nil
	}
	ui.PrintSeparator()

	// ── Phase 2: SSD → NAS ────────────────────────────────────────────────────
	return runSync(cfg, logger, syncOptions{order: cfg.SyncOrder()})
}

// runSync copies files from SSD that are missing on the NAS.
// opts filters the batch to one category (videosOnly/photosOnly) and picks the
// transfer order: videos first by default so they are prioritised if the
// connection is lost mid-run, or smallest-first with order=size-asc so the
// most-likely-to-succeed files go first on a flaky connection.
// It is called by both the copy command (phase 2) and the standalone sync command.
func runSync(cfg *config.Config, logger *log.Logger, opts syncOptions) error {
	exts := cfg.NormalisedExtensions()
	categoryFn := func(f scan.FileInfo) string { return cfg.Category(f.RelPath) }

	ui.Bold.Println("  SSD → NAS")
	fmt.Println("  ─────────────────────────────────────────")

	nasPhotosAvail := config.RootAvailable(cfg.NASPhotos)
	nasVideosAvail := config.RootAvailable(cfg.NASVideos)
	if !nasPhotosAvail && !nasVideosAvail {
		fmt.Println()
		ui.Yellow.Printf("  NAS not available at %s or %s\n", cfg.NASPhotos, cfg.NASVideos)
		ui.Yellow.Println("  Connect to VPN or ensure the NAS share is mounted, then run:")
		ui.Dim.Println("    camera-backup sync")
		logger.Println("SSD→NAS skipped: NAS not available")
		return nil
	}

	ssdPhotoFiles, ssdVideoFiles := scan.WalkDual(cfg.SSDPhotos, cfg.SSDVideos, exts)
	var ssdAll []scan.FileInfo
	if cfg.SSDMerged() {
		ssdAll = ssdPhotoFiles
	} else {
		ssdAll = append(append([]scan.FileInfo{}, ssdPhotoFiles...), ssdVideoFiles...)
	}
	photos, videos := scan.SplitByCategory(ssdAll, categoryFn)
	nasPhotoFiles, nasVideoFiles := scan.WalkDual(cfg.NASPhotos, cfg.NASVideos, exts)

	// Build tasks per category, videos first so large files are prioritised
	// if the connection drops. A category whose NAS root is unavailable is
	// skipped with a warning; duplicates across SSD roots are copied once.
	var tasks []copyop.Task
	addCategory := func(files []scan.FileInfo, cat string, avail bool, nasFiles []scan.FileInfo) {
		if opts.videosOnly && cat != "videos" {
			return
		}
		if opts.photosOnly && cat != "photos" {
			return
		}
		missing := scan.MissingByRelPath(files, scan.IndexByRelPath(nasFiles))
		if len(missing) == 0 {
			return
		}
		if !avail {
			ui.Yellow.Printf("  ⚠️  %d %s file(s) skipped — NAS %s root not available at %s\n",
				len(missing), cat, cat, cfg.NASRoot(cat))
			logger.Printf("SSD→NAS: %d %s files skipped, root unavailable", len(missing), cat)
			return
		}
		seen := map[string]bool{}
		for _, f := range missing {
			key := strings.ToLower(f.RelPath)
			if seen[key] {
				continue
			}
			seen[key] = true
			tasks = append(tasks, copyop.Task{Src: f, DstRoot: cfg.NASRoot(cat), DstRelPath: f.RelPath})
		}
	}
	addCategory(videos, "videos", nasVideosAvail, nasVideoFiles)
	addCategory(photos, "photos", nasPhotosAvail, nasPhotoFiles)
	if opts.order == config.OrderSizeAsc {
		copyop.SortBySizeAsc(tasks)
	}

	if len(tasks) == 0 {
		ui.Green.Println("\n  NAS is already up to date — nothing to copy.")
		logger.Println("NAS already up to date")
		return nil
	}

	if err := copyop.CheckSpace(tasks); err != nil {
		return err
	}
	switch {
	case opts.videosOnly:
		ui.Bold.Printf("\n  Copying %d video(s) to NAS...\n", len(tasks))
		logger.Println("SSD → NAS (videos only)")
	case opts.photosOnly:
		ui.Bold.Printf("\n  Copying %d photo(s) to NAS...\n", len(tasks))
		logger.Println("SSD → NAS (photos only)")
	case opts.order == config.OrderSizeAsc:
		ui.Bold.Printf("\n  Copying %d file(s) to NAS (smallest first)...\n", len(tasks))
		logger.Println("SSD → NAS (size ascending)")
	default:
		ui.Bold.Printf("\n  Copying %d file(s) to NAS (videos first)...\n", len(tasks))
		logger.Println("SSD → NAS")
	}
	errs := copyop.RunBatch(tasks, logger, false, cfg.NASWriteTimeout())
	fmt.Println()
	if errs > 0 {
		ui.Yellow.Printf("  ⚠️  %d file(s) failed — check the log.\n", errs)
	} else {
		ui.Green.Printf("  ✅  %d file(s) copied.\n", len(tasks))
	}
	return nil
}

func isDir(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func newTUICmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive TUI for camera backup",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, cleanup, err := initLogger()
			if err != nil {
				return err
			}
			defer cleanup()

			cfg, err := mustLoadConfig(*configPath)
			if err != nil {
				return err
			}

			m := tui.New(cfg, logger)
			p := tea.NewProgram(m, tea.WithAltScreen())
			m.SetProgram(p)
			_, err = p.Run()
			return err
		},
	}
}
