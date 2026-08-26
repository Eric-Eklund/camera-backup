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
	"github.com/Eric-Eklund/camera-backup/internal/devices"
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
  3. camera-backup status      — final check before formatting cards in-camera

To skip the local SSD entirely and dump a card or external drive straight to
the NAS, run "camera-backup dump" — or set direct_to_nas = true in config.toml
to make that the default for "copy" and the TUI.`,
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
		newDumpCmd(&configPath),
		newSyncCmd(&configPath),
		newVerifyCmd(&configPath),
		newDevicesCmd(&configPath),
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
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show device availability and file sync status",
		Long: `Scan the source device and both destinations and report what is missing where.

With --json the same scan is written to stdout as a single JSON object instead
of a table, for a status bar, a cron job or anything else that would otherwise
have to parse the human output. A count that this run did not work out — what
is missing on a bypassed SSD, or anything at all with no device mounted — is
null rather than zero, so "nothing is missing" cannot be confused with "this
was never compared".`,
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
			if !asJSON {
				return status.Run(cfg, logger)
			}
			r, err := status.Compute(cfg, logger)
			if err != nil {
				return err
			}
			return status.WriteJSON(cfg, r, cmd.OutOrStdout(), time.Now())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Write the scan to stdout as JSON")
	return cmd
}

// newDumpCmd copies straight from the source device to the NAS, skipping the
// local SSD. Unlike copy it does not depend on direct_to_nas — the setting only
// decides what copy and the TUI do by default.
func newDumpCmd(configPath *string) *cobra.Command {
	var opts syncOptions
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Copy missing files straight from the card/drive to the NAS (no local SSD)",
		Long: `Copy missing files straight from the source device to the NAS, bypassing
the local SSD. Use this when you plug in a memory card or an external drive
and want the files on the NAS without a local staging copy.

Files land in the same year/month/day layout as a normal backup and are
SHA256-verified after copying, because the NAS copy is the only copy.

The source device is the first mounted path of source/extra_sources, so one
config can serve a card reader and an external drive.`,
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
			if err := opts.resolveOrder(cfg); err != nil {
				return err
			}
			return runDirect(cfg, logger, opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.videosOnly, "videos-only", "v", false, "Only copy video files")
	cmd.Flags().BoolVarP(&opts.photosOnly, "photos-only", "p", false, "Only copy photo files")
	cmd.Flags().StringVar(&opts.order, "order", "",
		"Transfer order: videos-first or size-asc (smallest files first, best on flaky connections; default from nas_sync_order, else videos-first)")
	cmd.MarkFlagsMutuallyExclusive("videos-only", "photos-only")
	return cmd
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

// syncOptions controls which categories a NAS transfer covers and in what
// order. Shared by the sync and dump commands.
type syncOptions struct {
	videosOnly bool
	photosOnly bool
	order      string // config.OrderVideosFirst or config.OrderSizeAsc
}

// resolveOrder fills in the configured default order when --order was omitted
// and rejects an unknown value.
func (o *syncOptions) resolveOrder(cfg *config.Config) error {
	if o.order == "" {
		o.order = cfg.SyncOrder()
	}
	if o.order != config.OrderVideosFirst && o.order != config.OrderSizeAsc {
		return fmt.Errorf("invalid --order %q (valid: %s, %s)", o.order, config.OrderVideosFirst, config.OrderSizeAsc)
	}
	return nil
}

// skipsCategory reports whether cat is filtered out by --videos-only/--photos-only.
func (o *syncOptions) skipsCategory(cat string) bool {
	return (o.videosOnly && cat != "videos") || (o.photosOnly && cat != "photos")
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
			if err := opts.resolveOrder(cfg); err != nil {
				return err
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
	// direct_to_nas replaces the two-phase flow with a single source → NAS pass.
	if cfg.DirectToNAS {
		return runDirect(cfg, logger, syncOptions{order: cfg.SyncOrder()})
	}

	exts := cfg.NormalisedExtensions()
	categoryFn := func(f scan.FileInfo) string { return cfg.Category(f.RelPath) }

	source := cfg.ActiveSource()
	sourceAvail := isDir(source)
	ssdPhotosAvail := config.RootAvailable(cfg.SSDPhotos)
	ssdVideosAvail := config.RootAvailable(cfg.SSDVideos)

	// ── Phase 1: Camera → SSD ─────────────────────────────────────────────────
	ui.Bold.Println("\n  Phase 1: Camera → SSD")
	fmt.Println("  ─────────────────────────────────────────")

	phase1Ran := false
	if !sourceAvail {
		ui.Yellow.Printf("  Camera not available at %s — skipping.\n", source)
		ui.Yellow.Println("  To sync SSD → NAS only, run: camera-backup sync")
		logger.Println("Phase 1 skipped: camera not available")
	} else if !ssdPhotosAvail && !ssdVideosAvail {
		return fmt.Errorf("SSD not accessible at %s or %s", cfg.SSDPhotos, cfg.SSDVideos)
	} else {
		phase1Ran = true
		cameraFiles, err := scan.WalkSource(source, exts)
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

// runDirect copies files from the source device straight to the NAS, bypassing
// the local SSD. It is what the dump command runs, and what copy runs when
// direct_to_nas is set.
//
// Paths are transformed exactly as in Camera→SSD (year/month/day under the
// category root) so a direct dump and a staged backup produce the same tree,
// and every file is SHA256-verified: with no SSD in the chain the NAS copy is
// the only copy. NAS writes are bounded by nas_write_timeout_seconds so a hung
// mount fails the file instead of the run.
func runDirect(cfg *config.Config, logger *log.Logger, opts syncOptions) error {
	exts := cfg.NormalisedExtensions()
	categoryFn := func(f scan.FileInfo) string { return cfg.Category(f.RelPath) }
	source := cfg.ActiveSource()

	ui.Bold.Println("\n  Direct: Source → NAS (local SSD bypassed)")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Printf("  Source: %s\n", source)

	if !isDir(source) {
		return fmt.Errorf("source not available at %s — insert the card or connect the drive", source)
	}
	if !cfg.NASConfigured() {
		return fmt.Errorf("no NAS configured — set nas_photos and nas_videos in config.toml")
	}
	nasPhotosAvail := config.RootAvailable(cfg.NASPhotos)
	nasVideosAvail := config.RootAvailable(cfg.NASVideos)
	if !nasPhotosAvail && !nasVideosAvail {
		return fmt.Errorf("NAS not available at %s or %s — mount the share (or connect the VPN) and re-run",
			cfg.NASPhotos, cfg.NASVideos)
	}

	srcFiles, err := scan.WalkSource(source, exts)
	if err != nil {
		return err
	}
	srcFiles, unstable := scan.SplitStable(srcFiles, time.Now(), scan.StableAge)
	if len(unstable) > 0 {
		ui.Yellow.Printf("  ⚠️  %d file(s) skipped — modified moments ago, possibly still being written. Re-run when the device is idle.\n", len(unstable))
		logger.Printf("direct: %d file(s) skipped — modtime within %s of scan", len(unstable), scan.StableAge)
	}
	photos, videos := scan.SplitByCategory(srcFiles, categoryFn)
	nasPhotoFiles, nasVideoFiles := scan.WalkDual(cfg.NASPhotos, cfg.NASVideos, exts)

	// Build tasks per category, videos first so large files are prioritised if
	// the connection drops. A category whose NAS root is unavailable is skipped
	// with a warning so the other category still gets copied.
	var tasks []copyop.Task
	addCategory := func(files []scan.FileInfo, cat string, avail bool, nasFiles []scan.FileInfo) {
		if opts.skipsCategory(cat) {
			return
		}
		missing := scan.MissingFromDest(files, scan.IndexByRelPath(nasFiles))
		if len(missing) == 0 {
			return
		}
		if !avail {
			ui.Yellow.Printf("  ⚠️  %d %s file(s) skipped — NAS %s root not available at %s\n",
				len(missing), cat, cat, cfg.NASRoot(cat))
			logger.Printf("direct: %d %s files skipped, root unavailable", len(missing), cat)
			return
		}
		for _, f := range missing {
			tasks = append(tasks, copyop.Task{Src: f, DstRoot: cfg.NASRoot(cat), DstRelPath: f.DestRelPath()})
		}
	}
	addCategory(videos, "videos", nasVideosAvail, nasVideoFiles)
	addCategory(photos, "photos", nasPhotosAvail, nasPhotoFiles)
	if opts.order == config.OrderSizeAsc {
		copyop.SortBySizeAsc(tasks)
	}

	if len(tasks) == 0 {
		ui.Green.Println("\n  NAS is already up to date — nothing to copy.")
		logger.Println("direct: NAS already up to date")
		return nil
	}

	if err := copyop.CheckSpace(tasks); err != nil {
		return err
	}
	ui.Bold.Printf("\n  Copying %d file(s) straight to NAS (verified)...\n", len(tasks))
	logger.Printf("direct source→NAS: %d files, order=%s", len(tasks), opts.order)
	errs := copyop.RunBatch(tasks, logger, true, cfg.NASWriteTimeout())
	fmt.Println()
	if errs > 0 {
		ui.Red.Printf("  ❌  %d file(s) failed to copy — do not format the card.\n", errs)
		ui.Red.Println("  Check the log, fix the issue, and re-run.")
		return fmt.Errorf("%d file(s) failed during Source → NAS", errs)
	}
	ui.Green.Printf("  ✅  %d file(s) copied and verified.\n", len(tasks))
	return nil
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
		if opts.skipsCategory(cat) {
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

// newDevicesCmd lists what is mounted right now, so the mount point of a card
// or drive can be copied into config.toml instead of hunted for with lsblk.
// The TUI does the same from its device screen, where a device can also be
// picked outright.
func newDevicesCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List mounted devices that could serve as a source",
		Long: `List the filesystems mounted on this machine — card readers, USB drives,
internal disks and network shares — with the mount point to put in config.toml.

Removable devices come first, and a device holding a DCIM directory (a card
straight out of a camera) comes first of all. The device currently acting as
the source is marked.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			devs, err := devices.List()
			if err != nil {
				return fmt.Errorf("cannot list devices: %w", err)
			}

			// The config is only used to mark the active source, so a missing
			// or broken config still leaves a usable listing.
			active, configured := "", map[string]bool{}
			if cfg, err := config.Load(*configPath); err == nil {
				active = cfg.ActiveSource()
				for _, p := range cfg.SourceCandidates() {
					configured[p] = true
				}
			}

			if len(devs) == 0 {
				ui.Yellow.Println("\n  No usable devices found.")
				fmt.Println()
				return nil
			}

			fmt.Println()
			ui.Bold.Println("  Mounted devices")
			fmt.Println("  " + strings.Repeat("─", 72))
			for _, d := range devs {
				mark := " "
				switch {
				case d.Path == active:
					mark = ui.Green.Sprint("●")
				case configured[d.Path]:
					mark = ui.Dim.Sprint("○")
				}
				name := d.Name()
				if d.HasDCIM {
					name += " (DCIM)"
				}
				free := "—"
				if d.TotalBytes > 0 {
					free = devices.FormatBytes(d.FreeBytes) + " free"
				}
				fmt.Printf("  %s %-22s %-10s %-30s %s\n",
					mark, name, d.Kind, d.Path, ui.Dim.Sprint(free))
			}
			fmt.Println()
			ui.Dim.Println("  ● current source   ○ listed in config.toml")
			ui.Dim.Println("  Set one with: source = \"<mount point>\" in config.toml,")
			ui.Dim.Println("  or pick it live in the TUI with [d].")
			fmt.Println()
			return nil
		},
	}
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

			m := tui.New(cfg, logger, *configPath)
			p := tea.NewProgram(m, tea.WithAltScreen())
			m.SetProgram(p)
			_, err = p.Run()
			return err
		},
	}
}
