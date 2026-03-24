package verify

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Eric-Eklund/camera-backup/internal/checksum"
	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/ui"
)

type fileResult struct {
	relPath    string
	issues     []string
	cameraHash string
	ssdHash    string
	nasHash    string
}

// Run executes the verify command.
// If verbose is true every file is printed; otherwise only failures are shown.
func Run(cfg *config.Config, logger *log.Logger, verbose bool) error {
	exts := cfg.NormalisedExtensions()

	sourceAvail := isDir(cfg.Source)
	ssdAvail := isDir(cfg.SSD)
	nasAvail := cfg.NAS != "" && isDir(cfg.NAS)

	if !sourceAvail && !ssdAvail {
		return fmt.Errorf("neither camera nor SSD is available — nothing to verify")
	}

	var authorityFiles []scan.FileInfo
	var err error
	if sourceAvail {
		authorityFiles, err = scan.Walk(cfg.Source, exts)
	} else {
		ui.Yellow.Println("  Camera not available — verifying SSD vs NAS only.")
		authorityFiles, err = scan.Walk(cfg.SSD, exts)
	}
	if err != nil {
		return err
	}

	ssdIndex := map[string]scan.FileInfo{}
	if ssdAvail {
		ssdFiles, _ := scan.Walk(cfg.SSD, exts)
		ssdIndex = scan.IndexByRelPath(ssdFiles)
	}
	nasIndex := map[string]scan.FileInfo{}
	if nasAvail {
		nasFiles, _ := scan.Walk(cfg.NAS, exts)
		nasIndex = scan.IndexByRelPath(nasFiles)
	}

	fmt.Printf("\n  Verifying %d files...\n\n", len(authorityFiles))

	var results []fileResult

	for _, f := range authorityFiles {
		cat := cfg.Category(f.RelPath)
		r := fileResult{relPath: f.RelPath}

		// Camera hash.
		if sourceAvail {
			r.cameraHash, err = hashWithProgress(f.AbsPath, f.RelPath, "camera")
			if err != nil {
				r.issues = append(r.issues, fmt.Sprintf("camera read error: %v", err))
				logger.Printf("ERROR camera hash %s: %v", f.RelPath, err)
			}
		}

		// SSD hash.
		if ssdAvail {
			if ssd, ok := ssdIndex[f.DestKey(cat)]; ok {
				r.ssdHash, err = hashWithProgress(ssd.AbsPath, f.RelPath, "ssd")
				if err != nil {
					r.issues = append(r.issues, fmt.Sprintf("SSD read error: %v", err))
					logger.Printf("ERROR ssd hash %s: %v", f.RelPath, err)
				}
			} else {
				r.issues = append(r.issues, "missing from SSD")
			}
		}

		// NAS hash.
		if nasAvail {
			if nas, ok := nasIndex[f.DestKey(cat)]; ok {
				r.nasHash, err = hashWithProgress(nas.AbsPath, f.RelPath, "nas")
				if err != nil {
					r.issues = append(r.issues, fmt.Sprintf("NAS read error: %v", err))
					logger.Printf("ERROR nas hash %s: %v", f.RelPath, err)
				}
			} else {
				r.issues = append(r.issues, "missing from NAS")
			}
		}

		// Hash mismatch checks.
		if r.cameraHash != "" && r.ssdHash != "" && r.cameraHash != r.ssdHash {
			r.issues = append(r.issues, "SSD hash mismatch")
		}
		if r.cameraHash != "" && r.nasHash != "" && r.cameraHash != r.nasHash {
			r.issues = append(r.issues, "NAS hash mismatch")
		}
		if r.ssdHash != "" && r.nasHash != "" && r.ssdHash != r.nasHash {
			r.issues = append(r.issues, "SSD/NAS hash mismatch")
		}

		results = append(results, r)

		ok := len(r.issues) == 0
		logger.Printf("VERIFY %s camera=%s ssd=%s nas=%s ok=%v issues=%v",
			f.RelPath, short(r.cameraHash), short(r.ssdHash), short(r.nasHash), ok, r.issues)

		if verbose {
			if ok {
				ui.Green.Printf("  ✅  %s\n", filepath.Base(f.RelPath))
			} else {
				ui.Yellow.Printf("  ⚠️   %s — %v\n", filepath.Base(f.RelPath), r.issues)
			}
		} else if !ok {
			ui.Yellow.Printf("  ⚠️   %s — %v\n", filepath.Base(f.RelPath), r.issues)
		}
	}

	badCount := 0
	for _, r := range results {
		if len(r.issues) > 0 {
			badCount++
		}
	}
	fmt.Println()
	if badCount == 0 {
		ui.Green.Printf("  All %d files verified OK.\n\n", len(results))
	} else {
		ui.Yellow.Printf("  %d / %d files have issues.\n\n", badCount, len(results))
	}
	return nil
}

func hashWithProgress(absPath, relPath, location string) (string, error) {
	fi, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	label := fmt.Sprintf("[%s] %s", location, filepath.Base(relPath))
	pw := ui.NewProgressWriter(label, fi.Size(), os.Stdout)
	h, err := checksum.FileWithProgress(absPath, pw)
	pw.Done()
	return h, err
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func isDir(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
