package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/copyop"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/status"
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
		newVerifyCmd(&configPath),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// initLogger creates a timestamped log file in logs/ next to the binary.
func initLogger() (*log.Logger, func(), error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	logsDir := filepath.Join(filepath.Dir(exe), "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil, nil, err
	}
	stamp := time.Now().Format("2006-01-02_15-04-05")
	logPath := filepath.Join(logsDir, stamp+".log")
	f, err := os.Create(logPath)
	if err != nil {
		return nil, nil, err
	}
	logger := log.New(f, "", log.LstdFlags)
	logger.Printf("camera-backup started — log: %s", logPath)
	return logger, func() { f.Close() }, nil
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
	ssdAvail := isDir(cfg.SSD)

	// ── Phase 1: Camera → SSD ─────────────────────────────────────────────────
	ui.Bold.Println("\n  Phase 1: Camera → SSD")
	fmt.Println("  ─────────────────────────────────────────")

	if !sourceAvail {
		ui.Yellow.Printf("  Camera not available at %s — skipping.\n", cfg.Source)
		ui.Yellow.Println("  If you only need SSD → NAS, connect to your NAS and re-run.")
		logger.Println("Phase 1 skipped: camera not available")
	} else if !ssdAvail {
		return fmt.Errorf("SSD not accessible at %s", cfg.SSD)
	} else {
		cameraFiles, err := scan.Walk(cfg.Source, exts)
		if err != nil {
			return err
		}
		ssdFiles, _ := scan.Walk(cfg.SSD, exts)
		ssdIndex := scan.IndexByRelPath(ssdFiles)
		missing := scan.MissingFromDest(cameraFiles, ssdIndex, categoryFn)

		if len(missing) == 0 {
			ui.Green.Println("\n  SSD is already up to date — nothing to copy.")
			logger.Println("SSD already up to date")
		} else {
			tasks := make([]copyop.Task, len(missing))
			for i, f := range missing {
				tasks[i] = copyop.Task{Src: f, DstRelPath: f.DestRelPath(cfg.Category(f.RelPath))}
			}
			ui.Bold.Printf("\n  Copying %d file(s) to SSD...\n", len(tasks))
			errs := copyop.RunBatch(tasks, cfg.SSD, logger)
			fmt.Println()
			if errs > 0 {
				ui.Yellow.Printf("  ⚠️  %d file(s) failed — check the log.\n", errs)
			} else {
				ui.Green.Printf("  ✅  %d file(s) copied and verified.\n", len(tasks))
			}
		}
	}

	// ── Pause ─────────────────────────────────────────────────────────────────
	ui.PrintSeparator()
	ui.Bold.Println("  Camera backup to SSD is complete.")
	fmt.Println("  You may now disconnect and power off the camera.")
	fmt.Println()
	ui.Prompt("  Press Enter when ready to continue to NAS...")
	ui.PrintSeparator()

	// ── Phase 2: SSD → NAS (alla filer) ──────────────────────────────────────
	ui.Bold.Println("  Phase 2: SSD → NAS")
	fmt.Println("  ─────────────────────────────────────────")

	nasAvail := cfg.NAS != "" && isDir(cfg.NAS)
	if !nasAvail {
		fmt.Println()
		ui.Yellow.Printf("  NAS not available at %s\n", cfg.NAS)
		ui.Yellow.Println("  Connect to VPN or ensure the NAS drive is mapped, then re-run:")
		ui.Dim.Println("    camera-backup copy")
		ui.Dim.Println("  (Files already on SSD will be skipped automatically.)")
		logger.Println("Phase 2 skipped: NAS not available")
		return nil
	}

	// Rescan SSD after Phase 1 — include files just copied.
	ssdFilesNow, _ := scan.Walk(cfg.SSD, exts)
	nasFiles, _ := scan.Walk(cfg.NAS, exts)
	nasIndex := scan.IndexByRelPath(nasFiles)

	// SSD files already have category/date/filename as RelPath — compare directly.
	toNAS := scan.MissingByRelPath(ssdFilesNow, nasIndex)

	if len(toNAS) == 0 {
		ui.Green.Println("\n  NAS is already up to date — nothing to copy.")
		logger.Println("NAS already up to date")
		return nil
	}

	fmt.Println()
	if !ui.AskYesNo(fmt.Sprintf("  Copy %d file(s) to NAS? [y/N]: ", len(toNAS))) {
		ui.Dim.Println("  Skipped NAS copy.")
		logger.Println("NAS copy skipped by user")
		return nil
	}

	// For SSD→NAS the dest path equals the source RelPath (same structure).
	tasks := make([]copyop.Task, len(toNAS))
	for i, f := range toNAS {
		tasks[i] = copyop.Task{Src: f, DstRelPath: f.RelPath}
	}

	ui.Bold.Printf("\n  Copying %d file(s) to NAS...\n", len(tasks))
	logger.Println("Phase 2: SSD → NAS")
	errs := copyop.RunBatch(tasks, cfg.NAS, logger)
	fmt.Println()
	if errs > 0 {
		ui.Yellow.Printf("  ⚠️  %d file(s) failed — check the log.\n", errs)
	} else {
		ui.Green.Printf("  ✅  %d file(s) copied and verified.\n", len(tasks))
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
