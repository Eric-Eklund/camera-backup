package tui

import (
	"image"

	"github.com/Eric-Eklund/camera-backup/internal/copyop"
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

// phase2ReadyMsg carries freshly scanned SSD→NAS tasks after Phase 1 completes.
type phase2ReadyMsg struct {
	tasks []copyop.Task
	err   error
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

// progressTickMsg fires periodically while the progress screen is visible so
// speeds and ETA update even when no copy events arrive.
type progressTickMsg struct{}
