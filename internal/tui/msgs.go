package tui

import (
	"image"

	"github.com/Eric-Eklund/camera-backup/internal/copyop"
	"github.com/Eric-Eklund/camera-backup/internal/devices"
	"github.com/Eric-Eklund/camera-backup/internal/status"
	"github.com/Eric-Eklund/camera-backup/internal/verify"
)

type statusReadyMsg struct {
	result *status.StatusResult
	err    error
}

type fileProgressMsg struct {
	p copyop.FileProgress
}

type phase1DoneMsg struct {
	failures int
}

// phase2ReadyMsg carries freshly scanned SSD→NAS tasks after Phase 1
// completes, plus how many files were skipped because their NAS category
// root is unavailable.
type phase2ReadyMsg struct {
	tasks   []copyop.Task
	skipped int
}

type copyDoneMsg struct {
	failures int
}

type verifyFileMsg struct {
	done, total int
	result      verify.FileResult
}

type verifyDoneMsg struct {
	bad, total int
	// skipped names configured destinations that were not mounted, so the done
	// screen never presents an unchecked destination as verified.
	skipped []string
}

type thumbnailMsg struct {
	file string
	img  image.Image
	err  error
}

// fullImageMsg carries a full-resolution preview image (for ScreenPreview).
type fullImageMsg struct {
	file string
	img  image.Image
	err  error
}

type deviceChangedMsg struct{}

// devicesReadyMsg carries the result of a mounted-filesystem scan for the
// device picker.
type devicesReadyMsg struct {
	devs []devices.Device
	err  error
}

// progressTickMsg fires periodically while the progress screen is visible so
// speeds and ETA update even when no copy events arrive.
type progressTickMsg struct{}
