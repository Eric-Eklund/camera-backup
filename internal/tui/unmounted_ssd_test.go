package tui

// With no camera the TUI falls back to reading the SSD, and the SSD is then a
// *source*: its roots must exist as directories. An unmounted SSD whose
// mount-point parent still exists passes the destination rule (RootAvailable),
// scans as empty, and the screen answered `y` with "NAS is already up to
// date." — the CLI-sync lie, shown in the TUI instead. These tests pin the
// fixed behaviour: no reassuring answer for a comparison that never ran.

import (
	"io"
	"log"
	"strings"
	"testing"

	"github.com/Eric-Eklund/lumen/internal/config"
	"github.com/Eric-Eklund/lumen/internal/status"
)

// unmountedSSDModel is the state after a scan with no camera and an SSD whose
// roots do not exist but whose parent does: Avail (the destination rule) says
// yes, Readable (the source rule) says no.
func unmountedSSDModel() *Model {
	return &Model{
		cfg: &config.Config{
			Source:          "/gone/camera",
			SSDPhotos:       "/mnt/ssd/photos",
			SSDVideos:       "/mnt/ssd/videos",
			NASPhotos:       "/mnt/nas/photos",
			NASVideos:       "/mnt/nas/videos",
			FileExtensions:  []string{".NEF", ".MOV"},
			VideoExtensions: []string{".MOV"},
		},
		logger:   log.New(io.Discard, "", 0),
		selected: map[string]bool{},
		status: &status.StatusResult{
			SourceAvail:       false,
			SSDPhotosAvail:    true,
			SSDVideosAvail:    true,
			SSDPhotosReadable: false,
			SSDVideosReadable: false,
			NASPhotosAvail:    true,
			NASVideosAvail:    true,
		},
	}
}

func TestStartCopy_UnmountedSSDDoesNotClaimUpToDate(t *testing.T) {
	m := unmountedSSDModel()

	_, _ = m.startCopy()

	if strings.Contains(m.statusMsg, "up to date") {
		t.Fatalf("statusMsg = %q — reassurance for an SSD nothing ever read", m.statusMsg)
	}
	if !strings.Contains(m.statusMsg, "SSD is not mounted") {
		t.Errorf("statusMsg = %q, want it to say the SSD is not mounted", m.statusMsg)
	}
}

// The "All (0)" tab is the same statement in another place: it presents an
// unread SSD as a scanned, empty device.
func TestBuildTabs_UnmountedSSDShowsNoAllTab(t *testing.T) {
	m := unmountedSSDModel()

	m.buildTabs()

	for _, tab := range m.tabs {
		if strings.HasPrefix(tab, "All") {
			t.Errorf("tabs = %v — an All tab for an SSD that was never read", m.tabs)
		}
	}
}

// A mounted SSD keeps working exactly as before.
func TestStartCopy_ReadableSSDStillOffersTheSyncPath(t *testing.T) {
	m := unmountedSSDModel()
	m.status.SSDPhotosReadable = true
	m.status.SSDVideosReadable = true

	_, _ = m.startCopy()

	// Nothing is missing in this fixture, so the truthful answer here really
	// is "up to date" — the point is that it comes from a comparison that ran.
	if !strings.Contains(m.statusMsg, "up to date") {
		t.Errorf("statusMsg = %q, want the up-to-date answer for a readable, fully synced SSD", m.statusMsg)
	}
}
