package status_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Eric-Eklund/lumen/internal/status"
)

func (f *fixture) report() status.Report {
	f.t.Helper()
	return status.NewReport(f.cfg, f.compute(), time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC))
}

// decode runs the report through JSON and back into a generic map, so the
// assertions are about what a consumer actually receives.
func (f *fixture) decoded() map[string]any {
	f.t.Helper()
	var buf bytes.Buffer
	if err := status.WriteJSON(f.cfg, f.compute(), &buf, time.Now()); err != nil {
		f.t.Fatalf("WriteJSON: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		f.t.Fatalf("the output is not valid JSON: %v\n%s", err, buf.String())
	}
	return out
}

// The field names are what other people's panels are built on. This test is
// here to make renaming one a deliberate act rather than a refactor.
func TestReport_Schema(t *testing.T) {
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1024)
	got := f.decoded()

	want := []string{"bytes", "compared", "counts", "generated_at", "mode", "nas", "source", "ssd"}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != len(want) {
		t.Fatalf("top-level keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("top-level keys = %v, want %v", keys, want)
		}
	}

	counts := got["counts"].(map[string]any)
	for _, k := range []string{"source_files", "missing_on_ssd", "missing_on_nas", "unstable", "unreadable"} {
		if _, ok := counts[k]; !ok {
			t.Errorf("counts is missing %q", k)
		}
	}
	byteCounts := got["bytes"].(map[string]any)
	for _, k := range []string{"source_files", "to_ssd", "to_nas"} {
		if _, ok := byteCounts[k]; !ok {
			t.Errorf("bytes is missing %q", k)
		}
	}
	ssd := got["ssd"].(map[string]any)
	for _, k := range []string{"configured", "in_use", "merged", "photos", "videos"} {
		if _, ok := ssd[k]; !ok {
			t.Errorf("ssd is missing %q", k)
		}
	}
	photos := ssd["photos"].(map[string]any)
	for _, k := range []string{"path", "available", "free_bytes"} {
		if _, ok := photos[k]; !ok {
			t.Errorf("ssd.photos is missing %q", k)
		}
	}
}

func TestReport_CountsAndBytes(t *testing.T) {
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1000)
	f.camera("DSC_0002.JPG", 500)
	f.dest("ssd/photos", "DSC_0001.NEF", 1000)

	rep := f.report()

	if rep.Compared != status.ComparedSource {
		t.Errorf("compared = %q, want %q", rep.Compared, status.ComparedSource)
	}
	if rep.Mode != "staged" {
		t.Errorf("mode = %q, want staged", rep.Mode)
	}
	if rep.Counts.SourceFiles != 2 || rep.Bytes.SourceFiles != 1500 {
		t.Errorf("source = %d files / %d bytes, want 2 / 1500", rep.Counts.SourceFiles, rep.Bytes.SourceFiles)
	}
	if rep.Counts.MissingOnSSD == nil || *rep.Counts.MissingOnSSD != 1 {
		t.Errorf("missing_on_ssd = %v, want 1", rep.Counts.MissingOnSSD)
	}
	if rep.Bytes.ToSSD == nil || *rep.Bytes.ToSSD != 500 {
		t.Errorf("to_ssd = %v, want 500", rep.Bytes.ToSSD)
	}
	if rep.Counts.MissingOnNAS == nil || *rep.Counts.MissingOnNAS != 2 {
		t.Errorf("missing_on_nas = %v, want 2", rep.Counts.MissingOnNAS)
	}
}

// In direct mode the SSD is never compared, so its count is null. Reporting 0
// would tell a panel the SSD is up to date.
func TestReport_DirectModeSSDIsNull(t *testing.T) {
	f := newFixture(t)
	f.cfg.DirectToNAS = true
	f.camera("DSC_0001.NEF", 1024)

	rep := f.report()

	if rep.Mode != "direct" {
		t.Errorf("mode = %q, want direct", rep.Mode)
	}
	if rep.SSD.InUse {
		t.Error("ssd.in_use = true in direct mode")
	}
	if rep.Counts.MissingOnSSD != nil || rep.Bytes.ToSSD != nil {
		t.Errorf("missing_on_ssd = %v, to_ssd = %v — want null, the SSD was not compared",
			rep.Counts.MissingOnSSD, rep.Bytes.ToSSD)
	}
	if rep.Counts.MissingOnNAS == nil {
		t.Error("missing_on_nas is null although the NAS was compared")
	}
}

// With no card mounted the SSD stands in as the comparison source, and the
// report says so.
func TestReport_NoSourceComparesSSD(t *testing.T) {
	f := newFixture(t)
	f.cfg.Source = filepath.Join(f.dir, "not-mounted")
	f.dest("ssd/photos", "DSC_0001.NEF", 1024)

	rep := f.report()

	if rep.Compared != status.ComparedSSD {
		t.Errorf("compared = %q, want %q", rep.Compared, status.ComparedSSD)
	}
	if rep.Source.Available {
		t.Error("source.available = true for a device that is not mounted")
	}
	if rep.Source.FreeBytes != nil {
		t.Errorf("free_bytes = %v, want null for an unmounted device", rep.Source.FreeBytes)
	}
	if rep.Counts.MissingOnSSD != nil {
		t.Error("missing_on_ssd is not null although there was no source to compare")
	}
	if rep.Counts.MissingOnNAS == nil || *rep.Counts.MissingOnNAS != 1 {
		t.Errorf("missing_on_nas = %v, want 1", rep.Counts.MissingOnNAS)
	}
}

// An unmounted SSD whose mount-point parent still exists must not be reported
// as compared: a waybar panel reading missing_on_nas would show "0 → NAS" for
// a comparison that never ran. compared says "none" and the counts stay null.
func TestReport_UnmountedSSDComparesNothing(t *testing.T) {
	f := newFixture(t)
	f.cfg.Source = filepath.Join(f.dir, "not-mounted")
	for _, p := range []string{f.cfg.SSDPhotos, f.cfg.SSDVideos} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}

	rep := f.report()

	if rep.Compared != status.ComparedNone {
		t.Errorf("compared = %q, want %q — no SSD root exists to read", rep.Compared, status.ComparedNone)
	}
	if rep.Counts.MissingOnNAS != nil {
		t.Errorf("missing_on_nas = %v, want null for a comparison that never ran", *rep.Counts.MissingOnNAS)
	}
}

// Nothing mounted at all: every comparison is null, and the report says why.
func TestReport_NothingToCompare(t *testing.T) {
	f := newFixture(t)
	f.cfg.Source = filepath.Join(f.dir, "not-mounted")
	f.cfg.SSDPhotos = filepath.Join(f.dir, "gone", "photos")
	f.cfg.SSDVideos = filepath.Join(f.dir, "gone", "videos")

	rep := f.report()

	if rep.Compared != status.ComparedNone {
		t.Errorf("compared = %q, want %q", rep.Compared, status.ComparedNone)
	}
	if rep.Counts.MissingOnSSD != nil || rep.Counts.MissingOnNAS != nil {
		t.Error("a count survived although nothing was compared")
	}
}

func TestReport_MergedRoots(t *testing.T) {
	f := newFixture(t)
	merged := filepath.Join(f.dir, "ssd", "all")
	f.cfg.SSDPhotos, f.cfg.SSDVideos = merged, merged

	rep := f.report()

	if !rep.SSD.Merged {
		t.Error("ssd.merged = false although both roots are the same path")
	}
	if rep.NAS.Merged {
		t.Error("nas.merged = true although its roots differ")
	}
}

// An unconfigured device reports no free space rather than a zero that reads
// like a full disk.
func TestReport_UnconfiguredNAS(t *testing.T) {
	f := newFixture(t)
	f.cfg.NASPhotos, f.cfg.NASVideos = "", ""

	rep := f.report()

	if rep.NAS.Configured || rep.NAS.InUse {
		t.Error("nas is reported as configured although both roots are empty")
	}
	if rep.NAS.Photos.Available || rep.NAS.Photos.FreeBytes != nil {
		t.Errorf("nas.photos = %+v, want unavailable with null free space", rep.NAS.Photos)
	}
}

func TestReport_UnstableFilesAreCounted(t *testing.T) {
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1024)
	f.write("camera/DCIM/100NIKON/DSC_0002.NEF", 1024, time.Now())

	if rep := f.report(); rep.Counts.Unstable != 1 {
		t.Errorf("unstable = %d, want 1", rep.Counts.Unstable)
	}
}
