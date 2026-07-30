package tui

import (
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/status"
)

func directCfg() *config.Config {
	return &config.Config{
		Source:          "/cam",
		NASPhotos:       "/nas/Photos",
		NASVideos:       "/nas/Videos",
		FileExtensions:  []string{".NEF", ".JPG", ".MOV"},
		VideoExtensions: []string{".MOV"},
		DirectToNAS:     true,
	}
}

func srcFile(relPath string, size int64) scan.FileInfo {
	return scan.FileInfo{
		RelPath: relPath,
		AbsPath: "/cam/" + relPath,
		Size:    size,
		ModTime: time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC),
	}
}

// A direct dump writes to the NAS category roots using the same date-based
// layout a Camera→SSD copy would produce.
func TestBuildDirectTasks_RoutesByCategoryWithDatePaths(t *testing.T) {
	cfg := directCfg()
	r := &status.StatusResult{NASPhotosAvail: true, NASVideosAvail: true}
	missing := []scan.FileInfo{
		srcFile("DCIM/100NIKON/DSC_0001.NEF", 100),
		srcFile("DCIM/100NIKON/VID_0001.MOV", 900),
	}

	tasks, skipped := buildDirectTasks(missing, cfg, r)
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(tasks) != 2 {
		t.Fatalf("len(tasks) = %d, want 2", len(tasks))
	}
	// Videos first by default, so a dropped connection has already moved the
	// large files.
	if tasks[0].DstRoot != "/nas/Videos" {
		t.Errorf("tasks[0].DstRoot = %q, want /nas/Videos", tasks[0].DstRoot)
	}
	if want := "2026/2026-03/2026-03-25/VID_0001.MOV"; tasks[0].DstRelPath != want {
		t.Errorf("tasks[0].DstRelPath = %q, want %q", tasks[0].DstRelPath, want)
	}
	if tasks[1].DstRoot != "/nas/Photos" {
		t.Errorf("tasks[1].DstRoot = %q, want /nas/Photos", tasks[1].DstRoot)
	}
	if want := "2026/2026-03/2026-03-25/DSC_0001.NEF"; tasks[1].DstRelPath != want {
		t.Errorf("tasks[1].DstRelPath = %q, want %q", tasks[1].DstRelPath, want)
	}
}

// One unmounted NAS root must not stop the other category from being copied.
func TestBuildDirectTasks_SkipsUnavailableRoot(t *testing.T) {
	cfg := directCfg()
	r := &status.StatusResult{NASPhotosAvail: true, NASVideosAvail: false}
	missing := []scan.FileInfo{
		srcFile("DCIM/DSC_0001.NEF", 100),
		srcFile("DCIM/VID_0001.MOV", 900),
		srcFile("DCIM/VID_0002.MOV", 900),
	}

	tasks, skipped := buildDirectTasks(missing, cfg, r)
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(tasks))
	}
	if tasks[0].DstRoot != "/nas/Photos" {
		t.Errorf("DstRoot = %q, want /nas/Photos", tasks[0].DstRoot)
	}
}

func TestBuildDirectTasks_SizeAscOrder(t *testing.T) {
	cfg := directCfg()
	cfg.NASSyncOrder = config.OrderSizeAsc
	r := &status.StatusResult{NASPhotosAvail: true, NASVideosAvail: true}
	missing := []scan.FileInfo{
		srcFile("DCIM/VID_0001.MOV", 3_000_000),
		srcFile("DCIM/DSC_0001.NEF", 200),
		srcFile("DCIM/DSC_0002.JPG", 50_000),
	}

	tasks, _ := buildDirectTasks(missing, cfg, r)
	if len(tasks) != 3 {
		t.Fatalf("len(tasks) = %d, want 3", len(tasks))
	}
	for i := 1; i < len(tasks); i++ {
		if tasks[i-1].Src.Size > tasks[i].Src.Size {
			t.Errorf("tasks are not size-ascending: %d before %d", tasks[i-1].Src.Size, tasks[i].Src.Size)
		}
	}
}

// A merged NAS root (photos and videos in one tree) still gets both categories.
func TestBuildDirectTasks_MergedNASRoot(t *testing.T) {
	cfg := directCfg()
	cfg.NASPhotos, cfg.NASVideos = "/nas/Media", "/nas/Media"
	r := &status.StatusResult{NASPhotosAvail: true, NASVideosAvail: true}
	missing := []scan.FileInfo{
		srcFile("DCIM/DSC_0001.NEF", 100),
		srcFile("DCIM/VID_0001.MOV", 900),
	}

	tasks, skipped := buildDirectTasks(missing, cfg, r)
	if skipped != 0 || len(tasks) != 2 {
		t.Fatalf("tasks = %d, skipped = %d, want 2 and 0", len(tasks), skipped)
	}
	for _, task := range tasks {
		if task.DstRoot != "/nas/Media" {
			t.Errorf("DstRoot = %q, want /nas/Media", task.DstRoot)
		}
	}
}
