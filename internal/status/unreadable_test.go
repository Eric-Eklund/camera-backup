package status_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Eric-Eklund/lumen/internal/scan"
	"github.com/Eric-Eklund/lumen/internal/status"
)

// A scan that could not read part of the card describes less than the whole
// device, and the report has to say so. Every other number in it — how many
// files were found, how many are missing — is then an understatement, and a
// consumer reading only those numbers concludes the backup is complete.
func TestReport_CountsUnreadableSourcePaths(t *testing.T) {
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1024)

	r := f.compute()
	r.SourceUnreadable = []scan.Unreadable{
		{Path: "/card/DCIM/101NIKON", Err: fs.ErrPermission},
		{Path: "/card/DCIM/102NIKON", Err: errors.New("input/output error")},
	}

	rep := status.NewReport(f.cfg, r, time.Now())
	if rep.Counts.Unreadable != 2 {
		t.Errorf("counts.unreadable = %d, want 2", rep.Counts.Unreadable)
	}
}

// Zero is the honest answer when the whole device was read, and it must stay a
// plain zero rather than becoming a null: unlike the missing counts, this
// comparison always happens.
func TestReport_UnreadableIsZeroOnACleanScan(t *testing.T) {
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1024)

	got := f.decoded()
	counts := got["counts"].(map[string]any)
	v, ok := counts["unreadable"]
	if !ok {
		t.Fatal("counts.unreadable is missing")
	}
	if v != float64(0) {
		t.Errorf("counts.unreadable = %v, want 0", v)
	}
}

// The count is reported in direct mode too. There the NAS copy is the only
// copy, so a file the scan never saw has no second chance anywhere.
func TestReport_CountsUnreadableInDirectMode(t *testing.T) {
	f := newFixture(t)
	f.cfg.DirectToNAS = true
	f.camera("DSC_0001.NEF", 1024)

	r := f.compute()
	r.SourceUnreadable = []scan.Unreadable{{Path: "/card/DCIM", Err: fs.ErrPermission}}

	rep := status.NewReport(f.cfg, r, time.Now())
	if rep.Mode != "direct" {
		t.Fatalf("mode = %q, want direct", rep.Mode)
	}
	if rep.Counts.Unreadable != 1 {
		t.Errorf("counts.unreadable = %d, want 1", rep.Counts.Unreadable)
	}
}

// Compute is where the source scan happens, so it is Compute that has to carry
// the list out. This is the end-to-end shape of the bug that started this:
// a card with an unreadable directory reported one file and no warning.
func TestCompute_CarriesUnreadableSourcePaths(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	f := newFixture(t)
	f.camera("DSC_0001.NEF", 1024)

	locked := filepath.Join(f.cfg.Source, "DCIM", "101NIKON")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(locked, "DSC_0002.NEF")
	if err := os.WriteFile(hidden, make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hidden, shootTime, shootTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	r := f.compute()
	if len(r.SourceUnreadable) != 1 {
		t.Fatalf("SourceUnreadable = %v, want the locked directory reported", r.SourceUnreadable)
	}
	if len(r.CameraFiles) != 1 {
		t.Errorf("CameraFiles = %d, want the readable file still scanned", len(r.CameraFiles))
	}
	if got := status.NewReport(f.cfg, r, time.Now()).Counts.Unreadable; got != 1 {
		t.Errorf("counts.unreadable = %d, want 1", got)
	}
}
