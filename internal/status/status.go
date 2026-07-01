package status

import (
	"log"
	"os"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/ui"
)

// StatusResult holds everything the TUI and CLI need from a status scan.
type StatusResult struct {
	SourceAvail bool
	SSDAvail    bool
	NASAvail    bool
	SourceFree  int64
	SSDFree     int64
	NASFree     int64
	CameraFiles []scan.FileInfo
	SSDFiles    []scan.FileInfo
	NASFiles    []scan.FileInfo
	MissingOnSSD []scan.FileInfo
	MissingOnNAS []scan.FileInfo
}

// Compute scans all devices and returns a StatusResult without printing anything.
func Compute(cfg *config.Config, logger *log.Logger) (*StatusResult, error) {
	exts := cfg.NormalisedExtensions()

	r := &StatusResult{
		SourceAvail: isDir(cfg.Source),
		SSDAvail:    isDir(cfg.SSD),
		NASAvail:    cfg.NAS != "" && isDir(cfg.NAS),
	}
	r.SourceFree = freeOrNeg(cfg.Source, r.SourceAvail)
	r.SSDFree    = freeOrNeg(cfg.SSD, r.SSDAvail)
	r.NASFree    = freeOrNeg(cfg.NAS, r.NASAvail)

	if r.SourceAvail {
		var err error
		r.CameraFiles, err = scan.Walk(cfg.Source, exts)
		if err != nil {
			return nil, err
		}
		logger.Printf("Camera: %d files found", len(r.CameraFiles))
	}

	ssdIndex := map[string]scan.FileInfo{}
	if r.SSDAvail {
		r.SSDFiles, _ = scan.Walk(cfg.SSD, exts)
		ssdIndex = scan.IndexByRelPath(r.SSDFiles)
		logger.Printf("SSD: %d files found", len(r.SSDFiles))
	}

	nasIndex := map[string]scan.FileInfo{}
	if r.NASAvail {
		r.NASFiles, _ = scan.Walk(cfg.NAS, exts)
		nasIndex = scan.IndexByRelPath(r.NASFiles)
		logger.Printf("NAS: %d files found", len(r.NASFiles))
	}

	categoryFn := func(f scan.FileInfo) string { return cfg.Category(f.RelPath) }

	if r.SourceAvail {
		r.MissingOnSSD = scan.MissingFromDest(r.CameraFiles, ssdIndex, categoryFn)
		r.MissingOnNAS = scan.MissingFromDest(r.CameraFiles, nasIndex, categoryFn)
	} else if r.SSDAvail {
		// No camera: compute what SSD files are missing on NAS.
		r.MissingOnNAS = scan.MissingByRelPath(r.SSDFiles, nasIndex)
	}

	return r, nil
}

// Run executes the status command: scans devices and prints a human-readable report.
func Run(cfg *config.Config, logger *log.Logger) error {
	r, err := Compute(cfg, logger)
	if err != nil {
		return err
	}

	ui.PrintDeviceTable([]ui.DeviceRow{
		{Name: "Camera  " + cfg.Source, Available: r.SourceAvail, FreeBytes: r.SourceFree},
		{Name: "SSD     " + cfg.SSD, Available: r.SSDAvail, FreeBytes: r.SSDFree},
		{Name: "NAS     " + cfg.NAS, Available: r.NASAvail, FreeBytes: r.NASFree},
	})

	if !r.SourceAvail {
		ui.Yellow.Println("  Camera not available — cannot scan files.")
		return nil
	}

	ssdInfo := ui.SpaceInfo{Avail: r.SSDAvail, ToBytes: totalSize(r.MissingOnSSD), FreeBytes: r.SSDFree}
	nasInfo := ui.SpaceInfo{Avail: r.NASAvail, ToBytes: totalSize(r.MissingOnNAS), FreeBytes: r.NASFree}

	ui.PrintSummary(len(r.CameraFiles), totalSize(r.CameraFiles), len(r.MissingOnSSD), len(r.MissingOnNAS), ssdInfo, nasInfo, r.NASAvail)

	logger.Printf("status: %d camera files, %d missing from SSD (%s), %d missing from NAS (%s)",
		len(r.CameraFiles),
		len(r.MissingOnSSD), ui.FormatBytes(ssdInfo.ToBytes),
		len(r.MissingOnNAS), ui.FormatBytes(nasInfo.ToBytes))
	return nil
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

func freeOrNeg(path string, avail bool) int64 {
	if !avail {
		return -1
	}
	n, err := ui.FreeSpace(path)
	if err != nil {
		return -1
	}
	return n
}
