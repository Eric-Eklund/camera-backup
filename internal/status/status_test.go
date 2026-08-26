package status_test

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/status"
)

// shootTime is the modtime given to every fixture file unless a test says
// otherwise: comfortably older than scan.StableAge, so nothing is held back as
// "still being written", and a fixed date so destination paths are predictable.
var shootTime = time.Date(2026, 3, 25, 14, 30, 0, 0, time.Local)

// fixture builds a camera/SSD/NAS layout under a temporary directory and the
// config that points at it.
type fixture struct {
	t   *testing.T
	dir string
	cfg *config.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir}
	f.cfg = &config.Config{
		Source:          filepath.Join(dir, "camera"),
		SSDPhotos:       filepath.Join(dir, "ssd", "photos"),
		SSDVideos:       filepath.Join(dir, "ssd", "videos"),
		NASPhotos:       filepath.Join(dir, "nas", "photos"),
		NASVideos:       filepath.Join(dir, "nas", "videos"),
		FileExtensions:  []string{".NEF", ".JPG", ".MOV"},
		VideoExtensions: []string{".MOV"},
	}
	for _, p := range []string{f.cfg.Source, f.cfg.SSDPhotos, f.cfg.SSDVideos, f.cfg.NASPhotos, f.cfg.NASVideos} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// write creates a file of size bytes at path (relative to the fixture root)
// with the given modtime.
func (f *fixture) write(rel string, size int, mod time.Time) string {
	f.t.Helper()
	path := filepath.Join(f.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		f.t.Fatal(err)
	}
	return path
}

// camera writes a file on the card, dest writes one where a copy would land:
// the year/month/day tree DestRelPath produces.
func (f *fixture) camera(name string, size int) {
	f.write("camera/DCIM/100NIKON/"+name, size, shootTime)
}

func (f *fixture) dest(root, name string, size int) {
	f.write(filepath.Join(root, shootTime.Format("2006"), shootTime.Format("2006-01"),
		shootTime.Format("2006-01-02"), name), size, shootTime)
}

func (f *fixture) compute() *status.StatusResult {
	f.t.Helper()
	r, err := status.Compute(f.cfg, log.New(io.Discard, "", 0))
	if err != nil {
		f.t.Fatalf("Compute: %v", err)
	}
	return r
}

func names(files []scan.FileInfo) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, filepath.Base(f.RelPath))
	}
	sort.Strings(out)
	return out
}

func assertNames(t *testing.T, what string, got []scan.FileInfo, want ...string) {
	t.Helper()
	g := names(got)
	sort.Strings(want)
	if len(g) != len(want) {
		t.Errorf("%s = %v, want %v", what, g, want)
		return
	}
	for i := range g {
		if g[i] != want[i] {
			t.Errorf("%s = %v, want %v", what, g, want)
			return
		}
	}
}

// The everyday case: a card with photos and videos, empty destinations.
func TestCompute_EverythingMissing(t *testing.T) {
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1024)
	f.camera("DSC_0002.JPG", 512)
	f.camera("VID_0001.MOV", 2048)

	r := f.compute()

	if !r.SourceAvail || !r.SSDAvail() || !r.NASAvail() {
		t.Fatalf("availability: source=%v ssd=%v nas=%v — all three exist",
			r.SourceAvail, r.SSDAvail(), r.NASAvail())
	}
	assertNames(t, "CameraFiles", r.CameraFiles, "DSC_0001.NEF", "DSC_0002.JPG", "VID_0001.MOV")
	assertNames(t, "MissingOnSSD", r.MissingOnSSD, "DSC_0001.NEF", "DSC_0002.JPG", "VID_0001.MOV")
	assertNames(t, "MissingOnNAS", r.MissingOnNAS, "DSC_0001.NEF", "DSC_0002.JPG", "VID_0001.MOV")
	if r.CameraUnstable != 0 {
		t.Errorf("CameraUnstable = %d, want 0", r.CameraUnstable)
	}
}

// A file already copied is not reported missing; one copied to only one of the
// two destinations is still missing from the other.
func TestCompute_AlreadyCopied(t *testing.T) {
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1024)
	f.camera("DSC_0002.JPG", 512)
	f.dest("ssd/photos", "DSC_0001.NEF", 1024)

	r := f.compute()

	assertNames(t, "MissingOnSSD", r.MissingOnSSD, "DSC_0002.JPG")
	assertNames(t, "MissingOnNAS", r.MissingOnNAS, "DSC_0001.NEF", "DSC_0002.JPG")
	assertNames(t, "SSDFiles", r.SSDFiles, "DSC_0001.NEF")
}

// Category comes from the extension, never from the directory a file sits in:
// a video already on the videos root counts as present even though photos and
// videos are compared against different roots.
func TestCompute_CategoryDecidedByExtension(t *testing.T) {
	f := newFixture(t)
	f.camera("VID_0001.MOV", 2048)
	f.camera("DSC_0001.NEF", 1024)
	f.dest("ssd/videos", "VID_0001.MOV", 2048)
	// The same name under the *photos* root must not count for the video.
	f.dest("nas/photos", "VID_0001.MOV", 2048)

	r := f.compute()

	assertNames(t, "MissingOnSSD", r.MissingOnSSD, "DSC_0001.NEF")
	assertNames(t, "MissingOnNAS", r.MissingOnNAS, "DSC_0001.NEF", "VID_0001.MOV")
}

// Pointing both keys of a device at one directory merges the categories. The
// merged root is scanned once, so its files must not be counted twice.
func TestCompute_MergedRootsCountedOnce(t *testing.T) {
	f := newFixture(t)
	merged := filepath.Join(f.dir, "ssd", "all")
	f.cfg.SSDPhotos, f.cfg.SSDVideos = merged, merged
	f.camera("DSC_0001.NEF", 1024)
	f.write(filepath.Join("ssd/all", shootTime.Format("2006"), shootTime.Format("2006-01"),
		shootTime.Format("2006-01-02"), "DSC_0001.NEF"), 1024, shootTime)

	r := f.compute()

	if !f.cfg.SSDMerged() {
		t.Fatal("fixture did not merge the SSD roots")
	}
	assertNames(t, "SSDFiles", r.SSDFiles, "DSC_0001.NEF")
	if len(r.MissingOnSSD) != 0 {
		t.Errorf("MissingOnSSD = %v, want nothing — the file is on the merged root", names(r.MissingOnSSD))
	}
}

// direct_to_nas takes the SSD out of the picture even when its roots are
// configured and mounted: reporting files as "missing on SSD" would describe
// work the tool is not going to do.
func TestCompute_DirectModeIgnoresSSD(t *testing.T) {
	f := newFixture(t)
	f.cfg.DirectToNAS = true
	f.camera("DSC_0001.NEF", 1024)

	r := f.compute()

	if len(r.MissingOnSSD) != 0 {
		t.Errorf("MissingOnSSD = %v, want nothing in direct mode", names(r.MissingOnSSD))
	}
	assertNames(t, "MissingOnNAS", r.MissingOnNAS, "DSC_0001.NEF")
}

// With no card connected the SSD becomes the comparison source, so `sync` can
// still be told what is missing on the NAS.
func TestCompute_NoCameraComparesSSDToNAS(t *testing.T) {
	f := newFixture(t)
	f.cfg.Source = filepath.Join(f.dir, "not-mounted")
	f.dest("ssd/photos", "DSC_0001.NEF", 1024)
	f.dest("ssd/photos", "DSC_0002.JPG", 512)
	f.dest("nas/photos", "DSC_0001.NEF", 1024)

	r := f.compute()

	if r.SourceAvail {
		t.Fatal("the source directory should not exist")
	}
	if r.SourceFree != -1 {
		t.Errorf("SourceFree = %d, want -1 for an unmounted device", r.SourceFree)
	}
	assertNames(t, "MissingOnNAS", r.MissingOnNAS, "DSC_0002.JPG")
}

// In direct mode the SSD is not a copy source either, so an absent card leaves
// nothing to compare.
func TestCompute_NoCameraInDirectMode(t *testing.T) {
	f := newFixture(t)
	f.cfg.DirectToNAS = true
	f.cfg.Source = filepath.Join(f.dir, "not-mounted")
	f.dest("ssd/photos", "DSC_0001.NEF", 1024)

	r := f.compute()

	if len(r.MissingOnNAS) != 0 {
		t.Errorf("MissingOnNAS = %v, want nothing — the SSD is not a source in direct mode", names(r.MissingOnNAS))
	}
}

// A file written seconds ago is probably still being written by the camera:
// copying it would produce a truncated destination, so it is held back and
// counted instead.
func TestCompute_UnstableFilesHeldBack(t *testing.T) {
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1024)
	f.write("camera/DCIM/100NIKON/DSC_0002.NEF", 1024, time.Now())

	r := f.compute()

	if r.CameraUnstable != 1 {
		t.Errorf("CameraUnstable = %d, want 1", r.CameraUnstable)
	}
	assertNames(t, "CameraFiles", r.CameraFiles, "DSC_0001.NEF")
	assertNames(t, "MissingOnSSD", r.MissingOnSSD, "DSC_0001.NEF")
}

// A destination root that does not exist yet, but whose parent does, is
// available: the root itself is created on the first copy. A root under an
// unmounted device fails both checks.
func TestCompute_RootAvailability(t *testing.T) {
	f := newFixture(t)
	f.cfg.NASPhotos = filepath.Join(f.dir, "nas", "Photos-new") // parent exists
	f.cfg.NASVideos = filepath.Join(f.dir, "unmounted", "Videos")

	r := f.compute()

	if !r.NASPhotosAvail {
		t.Error("NASPhotosAvail = false for a root whose parent exists")
	}
	if r.NASVideosAvail {
		t.Error("NASVideosAvail = true for a root under an unmounted device")
	}
	if !r.NASPartial() {
		t.Error("NASPartial() = false although exactly one root is available")
	}
	if r.NASVideosFree != -1 {
		t.Errorf("NASVideosFree = %d, want -1 when the root is unavailable", r.NASVideosFree)
	}
	if !r.NASRootAvail("photos") || r.NASRootAvail("videos") {
		t.Error("NASRootAvail disagrees with the per-root flags")
	}
}

// A file type left out of file_extensions is invisible to every command, so it
// must not show up as missing either.
func TestCompute_UnlistedExtensionsAreInvisible(t *testing.T) {
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1024)
	f.camera("NOTES.TXT", 10)
	f.camera("DSC_0003.CR2", 1024)

	r := f.compute()

	assertNames(t, "CameraFiles", r.CameraFiles, "DSC_0001.NEF")
	assertNames(t, "MissingOnSSD", r.MissingOnSSD, "DSC_0001.NEF")
}

// The scan follows source/extra_sources, so plugging in either device works
// without touching the config — and the result names the device it read.
func TestCompute_UsesActiveSource(t *testing.T) {
	f := newFixture(t)
	drive := filepath.Join(f.dir, "extdrive")
	f.cfg.Source = filepath.Join(f.dir, "not-mounted")
	f.cfg.ExtraSources = []string{drive}
	f.write("extdrive/DCIM/DSC_0001.NEF", 1024, shootTime)

	r := f.compute()

	if r.Source != drive {
		t.Errorf("Source = %q, want the mounted extra source %q", r.Source, drive)
	}
	if !r.SourceAvail {
		t.Error("SourceAvail = false although the extra source is mounted")
	}
	assertNames(t, "CameraFiles", r.CameraFiles, "DSC_0001.NEF")
}

// A device picked in the TUI takes precedence over the configured ones for
// that session.
func TestCompute_HonoursSourceOverride(t *testing.T) {
	f := newFixture(t)
	picked := filepath.Join(f.dir, "picked")
	f.write("picked/DCIM/DSC_9999.NEF", 2048, shootTime)
	f.camera("DSC_0001.NEF", 1024)
	f.cfg.SetSourceOverride(picked)

	r := f.compute()

	if r.Source != picked {
		t.Errorf("Source = %q, want the picked device %q", r.Source, picked)
	}
	assertNames(t, "CameraFiles", r.CameraFiles, "DSC_9999.NEF")
}
