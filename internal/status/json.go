package status

import (
	"encoding/json"
	"io"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
)

// The values Report.Compared can take: what the missing-file counts were
// worked out from.
const (
	// ComparedSource means the counts describe the mounted source device.
	ComparedSource = "source"
	// ComparedSSD means no source was mounted, so the SSD was compared
	// against the NAS instead — what `sync` would copy.
	ComparedSSD = "ssd"
	// ComparedNone means nothing could be compared: no source, and no SSD to
	// stand in for one.
	ComparedNone = "none"
)

// Report is the machine-readable form of a status scan.
//
// The field names are a contract: a waybar module, a cron job or a home
// automation sensor reads them, and renaming one silently breaks somebody's
// panel. Anything not computed in a given run is null rather than zero —
// "nothing is missing" and "this was never compared" are different answers,
// and a widget that cannot tell them apart will happily report a backup as
// complete when it was never checked.
type Report struct {
	GeneratedAt string `json:"generated_at"`
	// Mode is "staged" (camera → SSD → NAS) or "direct" (source → NAS).
	Mode string `json:"mode"`
	// Compared says what the counts below were derived from.
	Compared string `json:"compared"`

	Source Root       `json:"source"`
	SSD    Category   `json:"ssd"`
	NAS    Category   `json:"nas"`
	Counts Counts     `json:"counts"`
	Bytes  ByteCounts `json:"bytes"`
}

// Root is one device path and what is known about it.
type Root struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
	// FreeBytes is null when the device is unavailable or its free space
	// could not be read.
	FreeBytes *int64 `json:"free_bytes"`
}

// Category is a device's photo and video roots, which may be the same path.
type Category struct {
	// Configured is false when the device has no roots set at all.
	Configured bool `json:"configured"`
	// InUse is false for an SSD bypassed by direct mode, even when its roots
	// are configured and mounted.
	InUse bool `json:"in_use"`
	// Merged reports whether both categories point at the same directory.
	Merged bool `json:"merged"`
	Photos Root `json:"photos"`
	Videos Root `json:"videos"`
}

// Counts are file counts. The missing counts are null when that comparison
// did not happen in this run.
type Counts struct {
	SourceFiles  int  `json:"source_files"`
	MissingOnSSD *int `json:"missing_on_ssd"`
	MissingOnNAS *int `json:"missing_on_nas"`
	// Unstable counts source files skipped because they were written moments
	// ago and may still be in flight.
	Unstable int `json:"unstable"`
	// Unreadable counts paths on the source this scan could not open. Unlike
	// every other number here it does not describe files the scan looked at —
	// it says that source_files, and both missing counts, cover less than the
	// whole device. Anything above zero means this report cannot be read as a
	// complete picture, however encouraging the other counts look.
	Unreadable int `json:"unreadable"`
}

// ByteCounts mirror Counts in bytes, for a progress figure that means
// something on a card holding one video and three hundred photographs.
type ByteCounts struct {
	SourceFiles int64  `json:"source_files"`
	ToSSD       *int64 `json:"to_ssd"`
	ToNAS       *int64 `json:"to_nas"`
}

// NewReport turns a scan into its machine-readable form.
func NewReport(cfg *config.Config, r *StatusResult, now time.Time) Report {
	rep := Report{
		GeneratedAt: now.Format(time.RFC3339),
		Mode:        "staged",
		Compared:    ComparedNone,
		Source:      root(r.Source, r.SourceAvail, r.SourceFree),
		SSD: Category{
			Configured: cfg.SSDConfigured(),
			InUse:      cfg.SSDInUse(),
			Merged:     cfg.SSDConfigured() && cfg.SSDMerged(),
			Photos:     root(cfg.SSDPhotos, r.SSDPhotosAvail, r.SSDPhotosFree),
			Videos:     root(cfg.SSDVideos, r.SSDVideosAvail, r.SSDVideosFree),
		},
		NAS: Category{
			Configured: cfg.NASConfigured(),
			InUse:      cfg.NASConfigured(),
			Merged:     cfg.NASConfigured() && cfg.NASMerged(),
			Photos:     root(cfg.NASPhotos, r.NASPhotosAvail, r.NASPhotosFree),
			Videos:     root(cfg.NASVideos, r.NASVideosAvail, r.NASVideosFree),
		},
		Counts: Counts{
			SourceFiles: len(r.CameraFiles),
			Unstable:    r.CameraUnstable,
			Unreadable:  len(r.SourceUnreadable),
		},
		Bytes: ByteCounts{SourceFiles: totalSize(r.CameraFiles)},
	}
	if cfg.DirectToNAS {
		rep.Mode = "direct"
	}

	// Which comparisons ran is decided in Compute; mirror that here rather
	// than guessing, so a count is null exactly when it was not worked out.
	switch {
	case r.SourceAvail:
		rep.Compared = ComparedSource
		if cfg.SSDInUse() {
			rep.Counts.MissingOnSSD = intPtr(len(r.MissingOnSSD))
			rep.Bytes.ToSSD = int64Ptr(totalSize(r.MissingOnSSD))
		}
		rep.Counts.MissingOnNAS = intPtr(len(r.MissingOnNAS))
		rep.Bytes.ToNAS = int64Ptr(totalSize(r.MissingOnNAS))
	case r.SSDSourceReadable() && cfg.SSDInUse():
		rep.Compared = ComparedSSD
		rep.Counts.MissingOnNAS = intPtr(len(r.MissingOnNAS))
		rep.Bytes.ToNAS = int64Ptr(totalSize(r.MissingOnNAS))
	}
	return rep
}

// WriteJSON runs a scan and writes its report to w as JSON.
func WriteJSON(cfg *config.Config, r *StatusResult, w io.Writer, now time.Time) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(NewReport(cfg, r, now))
}

// root describes one configured path. An unconfigured path reports no free
// space rather than a misleading zero.
func root(path string, avail bool, free int64) Root {
	out := Root{Path: path, Available: path != "" && avail}
	if out.Available && free >= 0 {
		out.FreeBytes = int64Ptr(free)
	}
	return out
}

func intPtr(v int) *int       { return &v }
func int64Ptr(v int64) *int64 { return &v }
