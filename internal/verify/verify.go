package verify

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/Eric-Eklund/camera-backup/internal/checksum"
	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/ui"
)

// FileResult is the result for a single verified file.
type FileResult struct {
	RelPath string
	Issues  []string
}

// ProgressFn is called after each file is verified.
type ProgressFn func(done, total int, r FileResult)

// Run executes the verify command with CLI output.
// If verbose is true every file is printed; otherwise only failures are shown.
func Run(cfg *config.Config, logger *log.Logger, verbose bool) error {
	bad, total := 0, 0
	err := verifyAll(cfg, logger, os.Stdout, func(done, tot int, r FileResult) {
		total = tot
		ok := len(r.Issues) == 0
		if !ok {
			bad++
		}
		if verbose {
			if ok {
				ui.Green.Printf("  ✅  %s\n", filepath.Base(r.RelPath))
			} else {
				ui.Yellow.Printf("  ⚠️   %s — %v\n", filepath.Base(r.RelPath), r.Issues)
			}
		} else if !ok {
			ui.Yellow.Printf("  ⚠️   %s — %v\n", filepath.Base(r.RelPath), r.Issues)
		}
	})
	if err != nil {
		return err
	}

	fmt.Println()
	if bad == 0 {
		ui.Green.Printf("  All %d files verified OK.\n\n", total)
	} else {
		ui.Yellow.Printf("  %d / %d files have issues.\n\n", bad, total)
	}
	return nil
}

// RunWithCallback verifies all files without printing to stdout.
// fn is called after each file completes; fn may be nil.
func RunWithCallback(cfg *config.Config, logger *log.Logger, fn ProgressFn) error {
	return verifyAll(cfg, logger, nil, fn)
}

// verifyAll hashes every authority file (camera if available, else SSD) against
// its SSD and NAS copies. When progressOut is non-nil, CLI headers and per-hash
// progress bars are written to it; when nil the pass is silent.
// fn (may be nil) receives each file's result as it completes.
func verifyAll(cfg *config.Config, logger *log.Logger, progressOut io.Writer, fn ProgressFn) error {
	exts := cfg.NormalisedExtensions()

	sourceAvail := isDir(cfg.Source)
	ssdPhotosAvail := config.RootAvailable(cfg.SSDPhotos)
	ssdVideosAvail := config.RootAvailable(cfg.SSDVideos)
	nasPhotosAvail := config.RootAvailable(cfg.NASPhotos)
	nasVideosAvail := config.RootAvailable(cfg.NASVideos)

	if !sourceAvail && !ssdPhotosAvail && !ssdVideosAvail {
		return fmt.Errorf("neither camera nor SSD is available — nothing to verify")
	}

	// Destination indices, one per category root (shared when merged).
	ssdPhotoFiles, ssdVideoFiles := scan.WalkDual(cfg.SSDPhotos, cfg.SSDVideos, exts)
	nasPhotoFiles, nasVideoFiles := scan.WalkDual(cfg.NASPhotos, cfg.NASVideos, exts)
	ssdIndex := map[string]map[string]scan.FileInfo{
		"photos": scan.IndexByRelPath(ssdPhotoFiles),
		"videos": scan.IndexByRelPath(ssdVideoFiles),
	}
	nasIndex := map[string]map[string]scan.FileInfo{
		"photos": scan.IndexByRelPath(nasPhotoFiles),
		"videos": scan.IndexByRelPath(nasVideoFiles),
	}
	ssdCatAvail := map[string]bool{"photos": ssdPhotosAvail, "videos": ssdVideosAvail}
	nasCatAvail := map[string]bool{"photos": nasPhotosAvail, "videos": nasVideosAvail}

	var authorityFiles []scan.FileInfo
	var err error
	if sourceAvail {
		authorityFiles, err = scan.Walk(cfg.Source, exts)
		if err != nil {
			return err
		}
	} else {
		if progressOut != nil {
			ui.Yellow.Fprintln(progressOut, "  Camera not available — verifying SSD vs NAS only.")
		}
		if cfg.SSDMerged() {
			authorityFiles = ssdPhotoFiles
		} else {
			authorityFiles = append(append([]scan.FileInfo{}, ssdPhotoFiles...), ssdVideoFiles...)
		}
	}

	if progressOut != nil {
		fmt.Fprintf(progressOut, "\n  Verifying %d files...\n\n", len(authorityFiles))
	}

	hash := func(absPath, relPath, location string) (string, error) {
		if progressOut != nil {
			return hashWithProgress(progressOut, absPath, relPath, location)
		}
		return checksum.File(absPath)
	}

	total := len(authorityFiles)
	for i, f := range authorityFiles {
		cat := cfg.Category(f.RelPath)
		res := FileResult{RelPath: f.RelPath}

		var cameraHash, ssdHash, nasHash string

		if sourceAvail {
			cameraHash, err = hash(f.AbsPath, f.RelPath, "camera")
			if err != nil {
				res.Issues = append(res.Issues, fmt.Sprintf("camera read error: %v", err))
				logger.Printf("ERROR camera hash %s: %v", f.RelPath, err)
			}
		}
		if ssdCatAvail[cat] {
			if ssd, ok := ssdIndex[cat][f.DestKey()]; ok {
				ssdHash, err = hash(ssd.AbsPath, f.RelPath, "ssd")
				if err != nil {
					res.Issues = append(res.Issues, fmt.Sprintf("SSD read error: %v", err))
					logger.Printf("ERROR ssd hash %s: %v", f.RelPath, err)
				}
			} else {
				res.Issues = append(res.Issues, "missing from SSD")
			}
		}
		if nasCatAvail[cat] {
			if nas, ok := nasIndex[cat][f.DestKey()]; ok {
				nasHash, err = hash(nas.AbsPath, f.RelPath, "nas")
				if err != nil {
					res.Issues = append(res.Issues, fmt.Sprintf("NAS read error: %v", err))
					logger.Printf("ERROR nas hash %s: %v", f.RelPath, err)
				}
			} else {
				res.Issues = append(res.Issues, "missing from NAS")
			}
		}

		if cameraHash != "" && ssdHash != "" && cameraHash != ssdHash {
			res.Issues = append(res.Issues, "SSD hash mismatch")
		}
		if cameraHash != "" && nasHash != "" && cameraHash != nasHash {
			res.Issues = append(res.Issues, "NAS hash mismatch")
		}
		if ssdHash != "" && nasHash != "" && ssdHash != nasHash {
			res.Issues = append(res.Issues, "SSD/NAS hash mismatch")
		}

		ok := len(res.Issues) == 0
		logger.Printf("VERIFY %s camera=%s ssd=%s nas=%s ok=%v issues=%v",
			f.RelPath, short(cameraHash), short(ssdHash), short(nasHash), ok, res.Issues)

		if fn != nil {
			fn(i+1, total, res)
		}
	}
	return nil
}

func hashWithProgress(out io.Writer, absPath, relPath, location string) (string, error) {
	fi, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	label := fmt.Sprintf("[%s] %s", location, filepath.Base(relPath))
	pw := ui.NewProgressWriter(label, fi.Size(), out)
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
