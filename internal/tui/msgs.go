package tui

import (
	"image"
	"time"

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

type deviceChangedMsg struct{}

type tickMsg time.Time
