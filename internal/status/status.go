package status

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Eric-Eklund/lumen/internal/config"
	"github.com/Eric-Eklund/lumen/internal/scan"
	"github.com/Eric-Eklund/lumen/internal/ui"
)

// StatusResult holds everything the TUI and CLI need from a status scan.
// SSD and NAS each have two destination roots (photos/videos) which may be
// the same directory; availability and free space are tracked per root.
type StatusResult struct {
	// Source is the source device this scan used — the first mounted entry of
	// source/extra_sources (config.ActiveSource).
	Source      string
	SourceAvail bool
	SourceFree  int64

	SSDPhotosAvail bool
	SSDVideosAvail bool
	NASPhotosAvail bool
	NASVideosAvail bool

	// SSDPhotosReadable/SSDVideosReadable report whether each SSD root exists
	// as a directory — the bar for reading the SSD as a *source*, which is
	// what the no-camera comparison does. The Avail fields above use
	// RootAvailable's parent-counts rule, which is a destination rule (the
	// root is created on first copy): an unmounted SSD passes it and scans as
	// empty, and "0 missing" would then describe a comparison that never ran.
	SSDPhotosReadable bool
	SSDVideosReadable bool
	SSDPhotosFree     int64
	SSDVideosFree     int64
	NASPhotosFree     int64
	NASVideosFree     int64

	CameraFiles []scan.FileInfo
	SSDFiles    []scan.FileInfo // combined photos+videos roots (single scan when merged)
	NASFiles    []scan.FileInfo

	// CameraUnstable counts camera files skipped this scan because their
	// modtime is within scan.StableAge of now — probably still being written.
	CameraUnstable int

	// SourceUnreadable lists paths on the source device this scan could not
	// read. They are not missing files and not unstable ones: they are files
	// this run knows nothing about, so every count below describes less than
	// the whole device. Callers must show it.
	SourceUnreadable []scan.Unreadable

	// Per-root file lists, for lookups against a file's designated root.
	// For merged roots both slices are the same scan.
	SSDPhotoFiles []scan.FileInfo
	SSDVideoFiles []scan.FileInfo
	NASPhotoFiles []scan.FileInfo
	NASVideoFiles []scan.FileInfo

	MissingOnSSD []scan.FileInfo
	MissingOnNAS []scan.FileInfo
}

// SSDAvail reports whether at least one SSD root is mounted.
func (r *StatusResult) SSDAvail() bool { return r.SSDPhotosAvail || r.SSDVideosAvail }

// SSDSourceReadable reports whether the SSD can stand in as the comparison
// source: at least one root actually exists as a directory. This is what the
// no-camera branch of Compute — and everything presenting its result — must
// ask, not SSDAvail: parent-counts is for destinations.
func (r *StatusResult) SSDSourceReadable() bool {
	return r.SSDPhotosReadable || r.SSDVideosReadable
}

// NASAvail reports whether at least one NAS root is mounted.
func (r *StatusResult) NASAvail() bool { return r.NASPhotosAvail || r.NASVideosAvail }

// SSDPartial reports whether exactly one of the SSD roots is available.
func (r *StatusResult) SSDPartial() bool { return r.SSDPhotosAvail != r.SSDVideosAvail }

// NASPartial reports whether exactly one of the NAS roots is available.
func (r *StatusResult) NASPartial() bool { return r.NASPhotosAvail != r.NASVideosAvail }

// SSDRootAvail reports whether the SSD root for category is mounted.
func (r *StatusResult) SSDRootAvail(category string) bool {
	if category == "videos" {
		return r.SSDVideosAvail
	}
	return r.SSDPhotosAvail
}

// NASRootAvail reports whether the NAS root for category is mounted.
func (r *StatusResult) NASRootAvail(category string) bool {
	if category == "videos" {
		return r.NASVideosAvail
	}
	return r.NASPhotosAvail
}

// Compute scans all devices and returns a StatusResult without printing anything.
func Compute(cfg *config.Config, logger *log.Logger) (*StatusResult, error) {
	exts := cfg.NormalisedExtensions()

	source := cfg.ActiveSource()
	r := &StatusResult{
		Source:         source,
		SourceAvail:    isDir(source),
		SSDPhotosAvail: config.RootAvailable(cfg.SSDPhotos),
		SSDVideosAvail: config.RootAvailable(cfg.SSDVideos),
		NASPhotosAvail: config.RootAvailable(cfg.NASPhotos),
		NASVideosAvail: config.RootAvailable(cfg.NASVideos),

		SSDPhotosReadable: isDir(cfg.SSDPhotos),
		SSDVideosReadable: isDir(cfg.SSDVideos),
	}
	r.SourceFree = freeOrNeg(source, r.SourceAvail)
	r.SSDPhotosFree = freeOrNeg(cfg.SSDPhotos, r.SSDPhotosAvail)
	r.SSDVideosFree = freeOrNeg(cfg.SSDVideos, r.SSDVideosAvail)
	r.NASPhotosFree = freeOrNeg(cfg.NASPhotos, r.NASPhotosAvail)
	r.NASVideosFree = freeOrNeg(cfg.NASVideos, r.NASVideosAvail)

	if r.SourceAvail {
		var err error
		r.CameraFiles, r.SourceUnreadable, err = scan.WalkSource(source, exts)
		if err != nil {
			return nil, err
		}
		for _, u := range r.SourceUnreadable {
			logger.Printf("UNREADABLE source path %s", u)
		}
		var unstable []scan.FileInfo
		r.CameraFiles, unstable = scan.SplitStable(r.CameraFiles, time.Now(), scan.StableAge)
		r.CameraUnstable = len(unstable)
		if r.CameraUnstable > 0 {
			logger.Printf("Camera: %d file(s) skipped — modified within %s of scan, possibly still being written", r.CameraUnstable, scan.StableAge)
		}
		logger.Printf("Camera: %d files found, %d path(s) unreadable", len(r.CameraFiles), len(r.SourceUnreadable))
	}

	// Scan destination roots. WalkDual scans a merged root only once.
	ssdPhotoFiles, ssdVideoFiles := scan.WalkDual(cfg.SSDPhotos, cfg.SSDVideos, exts)
	nasPhotoFiles, nasVideoFiles := scan.WalkDual(cfg.NASPhotos, cfg.NASVideos, exts)
	r.SSDPhotoFiles, r.SSDVideoFiles = ssdPhotoFiles, ssdVideoFiles
	r.NASPhotoFiles, r.NASVideoFiles = nasPhotoFiles, nasVideoFiles
	r.SSDFiles = combineRoots(cfg.SSDMerged(), ssdPhotoFiles, ssdVideoFiles)
	r.NASFiles = combineRoots(cfg.NASMerged(), nasPhotoFiles, nasVideoFiles)
	if r.SSDAvail() {
		logger.Printf("SSD: %d files found", len(r.SSDFiles))
	}
	if r.NASAvail() {
		logger.Printf("NAS: %d files found", len(r.NASFiles))
	}

	ssdPhotoIdx := scan.IndexByRelPath(ssdPhotoFiles)
	ssdVideoIdx := scan.IndexByRelPath(ssdVideoFiles)
	nasPhotoIdx := scan.IndexByRelPath(nasPhotoFiles)
	nasVideoIdx := scan.IndexByRelPath(nasVideoFiles)

	categoryFn := func(f scan.FileInfo) string { return cfg.Category(f.RelPath) }

	if r.SourceAvail {
		camPhotos, camVideos := scan.SplitByCategory(r.CameraFiles, categoryFn)
		// In direct mode the SSD is out of the picture; with no SSD configured
		// at all every file would also look "missing from SSD".
		if cfg.SSDInUse() {
			r.MissingOnSSD = append(
				scan.MissingFromDest(camPhotos, ssdPhotoIdx),
				scan.MissingFromDest(camVideos, ssdVideoIdx)...)
		}
		r.MissingOnNAS = append(
			scan.MissingFromDest(camPhotos, nasPhotoIdx),
			scan.MissingFromDest(camVideos, nasVideoIdx)...)
	} else if r.SSDSourceReadable() && cfg.SSDInUse() {
		// No camera: compute what SSD files are missing on NAS, per category.
		// Skipped in direct mode — there the SSD is not a copy source — and
		// when no SSD root exists as a directory: an unmounted SSD scans as
		// empty, and "nothing missing on the NAS" would describe a comparison
		// that never happened.
		ssdPhotos, ssdVideos := scan.SplitByCategory(r.SSDFiles, categoryFn)
		r.MissingOnNAS = append(
			scan.MissingByRelPath(ssdPhotos, nasPhotoIdx),
			scan.MissingByRelPath(ssdVideos, nasVideoIdx)...)
	}

	return r, nil
}

// combineRoots merges the photo and video root scans into one list.
// For a merged root WalkDual returns the same slice twice — use it once.
func combineRoots(merged bool, photoFiles, videoFiles []scan.FileInfo) []scan.FileInfo {
	if merged {
		return photoFiles
	}
	return append(append([]scan.FileInfo{}, photoFiles...), videoFiles...)
}

// Run executes the status command: scans devices and prints a human-readable report.
func Run(cfg *config.Config, logger *log.Logger) error {
	r, err := Compute(cfg, logger)
	if err != nil {
		return err
	}

	if cfg.DirectToNAS {
		ui.Bold.Println("\n  Mode: direct — source → NAS (local SSD bypassed)")
	}

	rows := []ui.DeviceRow{
		{Name: "Source      " + r.Source, Available: r.SourceAvail, FreeBytes: r.SourceFree},
	}
	switch {
	case !cfg.SSDConfigured(): // nothing to show — no SSD roots configured
	case cfg.SSDMerged():
		rows = append(rows, ui.DeviceRow{Name: "SSD         " + cfg.SSDPhotos, Available: r.SSDPhotosAvail, FreeBytes: r.SSDPhotosFree})
	default:
		rows = append(rows,
			ui.DeviceRow{Name: "SSD photos  " + cfg.SSDPhotos, Available: r.SSDPhotosAvail, FreeBytes: r.SSDPhotosFree},
			ui.DeviceRow{Name: "SSD videos  " + cfg.SSDVideos, Available: r.SSDVideosAvail, FreeBytes: r.SSDVideosFree},
		)
	}
	if cfg.NASConfigured() {
		if cfg.NASMerged() {
			rows = append(rows, ui.DeviceRow{Name: "NAS         " + cfg.NASPhotos, Available: r.NASPhotosAvail, FreeBytes: r.NASPhotosFree})
		} else {
			rows = append(rows,
				ui.DeviceRow{Name: "NAS photos  " + cfg.NASPhotos, Available: r.NASPhotosAvail, FreeBytes: r.NASPhotosFree},
				ui.DeviceRow{Name: "NAS videos  " + cfg.NASVideos, Available: r.NASVideosAvail, FreeBytes: r.NASVideosFree},
			)
		}
	}
	ui.PrintDeviceTable(rows)

	if !r.SourceAvail {
		ui.Yellow.Printf("  Source not available at %s — cannot scan files.\n", r.Source)
		return nil
	}

	ssdInfo := ui.SpaceInfo{
		Avail:     r.SSDAvail(),
		Bypassed:  !cfg.SSDInUse(),
		ToBytes:   totalSize(r.MissingOnSSD),
		FreeBytes: minFree(r.SSDPhotosFree, r.SSDVideosFree),
	}
	nasInfo := ui.SpaceInfo{Avail: r.NASAvail(), ToBytes: totalSize(r.MissingOnNAS), FreeBytes: minFree(r.NASPhotosFree, r.NASVideosFree)}

	ui.PrintSummary(len(r.CameraFiles), totalSize(r.CameraFiles), len(r.MissingOnSSD), len(r.MissingOnNAS), ssdInfo, nasInfo, r.NASAvail())

	if r.CameraUnstable > 0 {
		ui.Yellow.Printf("  ⚠️  %d camera file(s) skipped — modified moments ago, possibly still being written. Re-run when the card is idle.\n", r.CameraUnstable)
	}
	ui.PrintUnreadable(r.SourceUnreadable)

	logger.Printf("status: %d camera files, %d missing from SSD (%s), %d missing from NAS (%s), %d unreadable",
		len(r.CameraFiles),
		len(r.MissingOnSSD), ui.FormatBytes(ssdInfo.ToBytes),
		len(r.MissingOnNAS), ui.FormatBytes(nasInfo.ToBytes),
		len(r.SourceUnreadable))
	return nil
}

// minFree returns the smallest known free-space value (ignoring unavailable
// roots, which are -1). Conservative when both roots share a filesystem.
func minFree(a, b int64) int64 {
	switch {
	case a < 0:
		return b
	case b < 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

func totalSize(files []scan.FileInfo) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

func isDir(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// freeOrNeg returns free space for path, falling back to the parent when the
// root itself hasn't been created yet. -1 when unavailable/undetermined.
func freeOrNeg(path string, avail bool) int64 {
	if !avail {
		return -1
	}
	n, err := ui.FreeSpace(path)
	if err != nil {
		if n, err = ui.FreeSpace(filepath.Dir(path)); err != nil {
			return -1
		}
	}
	return n
}
