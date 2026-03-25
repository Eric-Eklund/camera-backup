package status

import (
	"log"
	"os"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/ui"
)

func Run(cfg *config.Config, logger *log.Logger) error {
	exts := cfg.NormalisedExtensions()

	sourceAvail := isDir(cfg.Source)
	ssdAvail := isDir(cfg.SSD)
	nasAvail := cfg.NAS != "" && isDir(cfg.NAS)

	ui.PrintDeviceTable([]ui.DeviceRow{
		{Name: "Camera  " + cfg.Source, Available: sourceAvail, FreeBytes: freeOrNeg(cfg.Source, sourceAvail)},
		{Name: "SSD     " + cfg.SSD, Available: ssdAvail, FreeBytes: freeOrNeg(cfg.SSD, ssdAvail)},
		{Name: "NAS     " + cfg.NAS, Available: nasAvail, FreeBytes: freeOrNeg(cfg.NAS, nasAvail)},
	})

	if !sourceAvail {
		ui.Yellow.Println("  Camera not available — cannot scan files.")
		return nil
	}

	cameraFiles, err := scan.Walk(cfg.Source, exts)
	if err != nil {
		return err
	}
	logger.Printf("Camera: %d files found", len(cameraFiles))

	// Build destination indexes (RelPath already includes category/date).
	ssdIndex := map[string]scan.FileInfo{}
	if ssdAvail {
		ssdFiles, _ := scan.Walk(cfg.SSD, exts)
		ssdIndex = scan.IndexByRelPath(ssdFiles)
		logger.Printf("SSD: %d files found", len(ssdFiles))
	}
	nasIndex := map[string]scan.FileInfo{}
	if nasAvail {
		nasFiles, _ := scan.Walk(cfg.NAS, exts)
		nasIndex = scan.IndexByRelPath(nasFiles)
		logger.Printf("NAS: %d files found", len(nasFiles))
	}

	categoryFn := func(f scan.FileInfo) string { return cfg.Category(f.RelPath) }

	missingSSD := scan.MissingFromDest(cameraFiles, ssdIndex, categoryFn)
	missingNAS := scan.MissingFromDest(cameraFiles, nasIndex, categoryFn)

	ssdInfo := ui.SpaceInfo{Avail: ssdAvail, ToBytes: totalSize(missingSSD), FreeBytes: freeOrNeg(cfg.SSD, ssdAvail)}
	nasInfo := ui.SpaceInfo{Avail: nasAvail, ToBytes: totalSize(missingNAS), FreeBytes: freeOrNeg(cfg.NAS, nasAvail)}

	ui.PrintSummary(len(cameraFiles), totalSize(cameraFiles), len(missingSSD), len(missingNAS), ssdInfo, nasInfo, nasAvail)

	logger.Printf("status: %d camera files, %d missing from SSD (%s), %d missing from NAS (%s)",
		len(cameraFiles),
		len(missingSSD), ui.FormatBytes(ssdInfo.ToBytes),
		len(missingNAS), ui.FormatBytes(nasInfo.ToBytes))
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
