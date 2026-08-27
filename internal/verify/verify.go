package verify

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

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

// Outcome is what a pass could not look at. Both fields have to reach the user
// with the result: a count of verified files presented on its own stands for
// destinations nothing compared against and for files nothing could read.
type Outcome struct {
	// UnmountedRoots names configured destinations that were not mounted, and
	// so were not compared against.
	UnmountedRoots []string
	// Unreadable lists paths on the authority device the scan could not read.
	// Those files were never hashed and appear nowhere else in the result —
	// not as OK, not as a failure, not in the total.
	Unreadable []scan.Unreadable
}

// Clean reports whether the pass saw everything it was configured to see.
func (o Outcome) Clean() bool {
	return len(o.UnmountedRoots) == 0 && len(o.Unreadable) == 0
}

// ProgressFn is called after each file is verified.
type ProgressFn func(done, total int, r FileResult)

// Run executes the verify command with CLI output.
// If verbose is true every file is printed; otherwise only failures are shown.
func Run(cfg *config.Config, logger *log.Logger, verbose bool) error {
	bad, total := 0, 0
	outcome, err := verifyAll(cfg, logger, os.Stdout, func(done, tot int, r FileResult) {
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
	switch {
	case bad == 0 && outcome.Clean():
		ui.Green.Printf("  All %d files verified OK.\n\n", total)
	case bad == 0:
		// Never let an unchecked destination, or a file nothing could read,
		// pass for a clean bill of health.
		ui.Green.Printf("  All %d files verified OK against what could be checked.\n", total)
	default:
		ui.Yellow.Printf("  %d / %d files have issues.\n", bad, total)
	}
	if len(outcome.UnmountedRoots) > 0 {
		ui.Yellow.Printf("  ⚠️  Not checked: %s — mount and re-run to verify there.\n", strings.Join(outcome.UnmountedRoots, ", "))
	}
	ui.PrintUnreadable(outcome.Unreadable)
	if bad != 0 || !outcome.Clean() {
		fmt.Println()
	}
	return runError(bad, total, outcome)
}

// runError decides the command's exit status. The summary above is for whoever
// is watching the terminal; a cron job or a `verify && …` chain sees only this.
// A pass that found mismatches, or that never read part of the source, must not
// exit 0 — "verify succeeded" would then stand for a backup that is corrupt, or
// for photographs nothing could open.
//
// An unmounted destination alone stays exit 0: skipping it is the documented
// "skipped, not failed" behaviour (verifying camera vs SSD away from the NAS is
// a normal run, not a broken one), and the summary names what went unchecked.
func runError(bad, total int, outcome Outcome) error {
	if bad > 0 {
		return fmt.Errorf("%d of %d file(s) failed verification — the backup is not confirmed", bad, total)
	}
	if n := len(outcome.Unreadable); n > 0 {
		return fmt.Errorf("%d source path(s) could not be read — those files were never verified; do not format the card", n)
	}
	return nil
}

// RunWithCallback verifies all files without printing to stdout.
// fn is called after each file completes; fn may be nil.
// The returned Outcome names what the pass could not look at — a pass that
// skipped a destination or could not read part of the source has not verified
// the backup, so callers must not present it as a clean result.
func RunWithCallback(cfg *config.Config, logger *log.Logger, fn ProgressFn) (Outcome, error) {
	return verifyAll(cfg, logger, nil, fn)
}

// verifyAll hashes every authority file (camera if available, else SSD) against
// its SSD and NAS copies. When progressOut is non-nil, CLI headers and per-hash
// progress bars are written to it; when nil the pass is silent.
// fn (may be nil) receives each file's result as it completes.
//
// It returns what it could not look at. An unmounted root is not an error — the
// other destinations are still worth checking — but silence about it would let
// "all files verified OK" stand for a destination nothing ever looked at, and
// the same is true of a source path the scan could not open.
func verifyAll(cfg *config.Config, logger *log.Logger, progressOut io.Writer, fn ProgressFn) (outcome Outcome, err error) {
	exts := cfg.NormalisedExtensions()

	source := cfg.ActiveSource()
	sourceAvail := isDir(source)
	// direct_to_nas takes the SSD out of the pipeline, so it is not a
	// destination this run should expect copies on and not an authority to
	// fall back to. Treating a configured-but-bypassed SSD as available made
	// verify report every directly dumped file as "missing from SSD" — six
	// failures out of seven on a backup that was in fact perfect.
	ssdInUse := cfg.SSDInUse()
	ssdPhotosAvail := ssdInUse && config.RootAvailable(cfg.SSDPhotos)
	ssdVideosAvail := ssdInUse && config.RootAvailable(cfg.SSDVideos)
	nasPhotosAvail := config.RootAvailable(cfg.NASPhotos)
	nasVideosAvail := config.RootAvailable(cfg.NASVideos)

	// As the fallback *authority* the SSD must exist as a directory — the
	// parent-counts rule above is for destinations, where the root is created
	// on first copy. An unmounted SSD passes that rule and scans as empty,
	// and the pass would end on "All 0 files verified OK" for a tree nothing
	// ever read.
	ssdPhotosReadable := ssdInUse && isDir(cfg.SSDPhotos)
	ssdVideosReadable := ssdInUse && isDir(cfg.SSDVideos)
	if !sourceAvail && !ssdPhotosReadable && !ssdVideosReadable {
		if ssdInUse {
			return outcome, fmt.Errorf("no verification authority available — mount the source device (%s) or an SSD root", source)
		}
		return outcome, fmt.Errorf("no verification authority available — mount the source device (%s); the local SSD is bypassed by direct_to_nas", source)
	}

	outcome.UnmountedRoots = unmountedRoots(cfg, map[string]bool{
		"photos": ssdPhotosAvail, "videos": ssdVideosAvail,
	}, map[string]bool{
		"photos": nasPhotosAvail, "videos": nasVideosAvail,
	})
	if len(outcome.UnmountedRoots) > 0 {
		logger.Printf("VERIFY skipped unmounted destinations: %s", strings.Join(outcome.UnmountedRoots, ", "))
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
	// Secondary lookup by basename+size, for copies that sit under a different
	// date than the authority now computes — files backed up before capture
	// times were read, or whose source modtime changed after the copy.
	ssdByNameSize := map[string]map[string]scan.FileInfo{
		"photos": scan.IndexByNameSize(ssdIndex["photos"]),
		"videos": scan.IndexByNameSize(ssdIndex["videos"]),
	}
	nasByNameSize := map[string]map[string]scan.FileInfo{
		"photos": scan.IndexByNameSize(nasIndex["photos"]),
		"videos": scan.IndexByNameSize(nasIndex["videos"]),
	}
	ssdCatAvail := map[string]bool{"photos": ssdPhotosAvail, "videos": ssdVideosAvail}
	nasCatAvail := map[string]bool{"photos": nasPhotosAvail, "videos": nasVideosAvail}

	var authorityFiles []scan.FileInfo
	if sourceAvail {
		authorityFiles, outcome.Unreadable, err = scan.WalkSource(source, exts)
		if err != nil {
			return outcome, err
		}
		for _, u := range outcome.Unreadable {
			logger.Printf("VERIFY UNREADABLE source path %s", u)
		}
	} else {
		if progressOut != nil {
			ui.Yellow.Fprintln(progressOut, "  Source device not available — verifying SSD vs NAS only.")
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
			if ssd, ok := findCopy(ssdIndex[cat], ssdByNameSize[cat], f); ok {
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
			if nas, ok := findCopy(nasIndex[cat], nasByNameSize[cat], f); ok {
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
	return outcome, nil
}

// unmountedRoots names the configured destination roots that are not currently
// available, so a caller can say which parts of the backup went unchecked.
// Merged roots are named once; an unconfigured device contributes nothing.
func unmountedRoots(cfg *config.Config, ssdAvail, nasAvail map[string]bool) []string {
	var out []string
	add := func(name, root string, avail bool) {
		if !avail {
			out = append(out, fmt.Sprintf("%s (%s)", name, root))
		}
	}
	// ssdAvail is already false for an SSD that is bypassed or unconfigured,
	// so there is nothing to name in either case.
	if cfg.SSDInUse() {
		if cfg.SSDMerged() {
			add("SSD", cfg.SSDPhotos, ssdAvail["photos"])
		} else {
			add("SSD photos", cfg.SSDPhotos, ssdAvail["photos"])
			add("SSD videos", cfg.SSDVideos, ssdAvail["videos"])
		}
	}
	if cfg.NASConfigured() {
		if cfg.NASMerged() {
			add("NAS", cfg.NASPhotos, nasAvail["photos"])
		} else {
			add("NAS photos", cfg.NASPhotos, nasAvail["photos"])
			add("NAS videos", cfg.NASVideos, nasAvail["videos"])
		}
	}
	return out
}

// findCopy locates f's copy in one destination root. It looks under the date
// path f computes now, then — because that path changed when capture times
// started being read — anywhere in the tree by basename+size. This mirrors how
// scan.MissingFromDest decides a file is already present, so verify never
// reports "missing" for a file copy would skip.
//
// The basename+size match is confirmed against the capture time, exactly as the
// copy path does it: three cards all number their first frame DSC_0001, and two
// of those can share a byte-exact size. Without the check a different photograph
// would be hashed and reported as a mismatch, when the truth is that this file
// was never copied.
func findCopy(byRelPath, byNameSize map[string]scan.FileInfo, f scan.FileInfo) (scan.FileInfo, bool) {
	if e, ok := findBySize(byRelPath, f.DestKey(), f.Size); ok {
		return e, true
	}
	e, ok := byNameSize[scan.NameSizeKey(f.RelPath, f.Size)]
	if !ok {
		return scan.FileInfo{}, false
	}
	if f.CaptureTime.IsZero() {
		// Nothing to compare — basename+size is the only evidence there is.
		return e, true
	}
	if t, got := scan.CaptureTime(e.AbsPath); got && !t.Equal(f.CaptureTime) {
		return scan.FileInfo{}, false
	}
	return e, true
}

// findBySize returns the destination entry for key whose size matches size:
// the original name first, then the _N collision variants that safeCreate
// allocates. Variants are contiguous, so probing stops at the first gap.
// A same-size entry can still be corrupt — the caller compares hashes.
// found is false when no entry of the wanted size exists (missing, or only a
// stray/partial file of a different size — which a copy run resolves as _N).
func findBySize(index map[string]scan.FileInfo, key string, size int64) (scan.FileInfo, bool) {
	if e, ok := index[key]; ok && e.Size == size {
		return e, true
	}
	ext := filepath.Ext(key)
	stem := strings.TrimSuffix(key, ext)
	for i := 1; ; i++ {
		e, ok := index[fmt.Sprintf("%s_%d%s", stem, i, ext)]
		if !ok {
			return scan.FileInfo{}, false
		}
		if e.Size == size {
			return e, true
		}
	}
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
