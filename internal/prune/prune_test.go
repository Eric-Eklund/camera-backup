package prune_test

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/prune"
)

var shot = time.Date(2026, 3, 25, 14, 30, 0, 0, time.Local)

type fixture struct {
	t   *testing.T
	dir string
	cfg *config.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir, cfg: &config.Config{
		Source:          filepath.Join(dir, "camera"),
		SSDPhotos:       filepath.Join(dir, "ssd", "photos"),
		SSDVideos:       filepath.Join(dir, "ssd", "videos"),
		NASPhotos:       filepath.Join(dir, "nas", "photos"),
		NASVideos:       filepath.Join(dir, "nas", "videos"),
		FileExtensions:  []string{".NEF", ".JPG", ".MOV"},
		VideoExtensions: []string{".MOV"},
	}}
	for _, p := range []string{f.cfg.SSDPhotos, f.cfg.SSDVideos, f.cfg.NASPhotos, f.cfg.NASVideos} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// put writes a file with the given content under root, in the date tree a copy
// would land in.
func (f *fixture) put(root, name, content string, date time.Time) string {
	f.t.Helper()
	path := filepath.Join(root, date.Format("2006"), date.Format("2006-01"), date.Format("2006-01-02"), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(path, date, date); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *fixture) build(opt prune.Options) *prune.Plan {
	f.t.Helper()
	p, err := prune.Build(f.cfg, log.New(io.Discard, "", 0), opt, nil)
	if err != nil {
		f.t.Fatalf("Build: %v", err)
	}
	return p
}

func deleted(p *prune.Plan) []string {
	out := make([]string, 0, len(p.Delete))
	for _, c := range p.Delete {
		out = append(out, filepath.Base(c.File.RelPath))
	}
	sort.Strings(out)
	return out
}

func keptReason(t *testing.T, p *prune.Plan, name string) prune.Reason {
	t.Helper()
	for _, c := range p.Keep {
		if filepath.Base(c.File.RelPath) == name {
			return c.Reason
		}
	}
	t.Fatalf("%s is not in the keep list", name)
	return ""
}

// A file whose NAS copy hashes identical can go; one the NAS has never seen
// stays.
func TestBuild_VerifiedGoesUnverifiedStays(t *testing.T) {
	f := newFixture(t)
	f.put(f.cfg.SSDPhotos, "DSC_0001.NEF", "one", shot)
	f.put(f.cfg.NASPhotos, "DSC_0001.NEF", "one", shot)
	f.put(f.cfg.SSDPhotos, "DSC_0002.NEF", "two", shot)

	p := f.build(prune.Options{})

	if got := deleted(p); len(got) != 1 || got[0] != "DSC_0001.NEF" {
		t.Fatalf("Delete = %v, want just the verified file", got)
	}
	if r := keptReason(t, p, "DSC_0002.NEF"); r != prune.ReasonMissing {
		t.Errorf("kept for %q, want %q", r, prune.ReasonMissing)
	}
	if p.Bytes != 3 {
		t.Errorf("Bytes = %d, want 3", p.Bytes)
	}
}

// Same name, same size, different content: this is the case the whole command
// exists to be careful about, and it must never be deleted.
func TestBuild_SameSizeDifferentContent(t *testing.T) {
	f := newFixture(t)
	f.put(f.cfg.SSDPhotos, "DSC_0001.NEF", "aaa", shot)
	f.put(f.cfg.NASPhotos, "DSC_0001.NEF", "bbb", shot)

	p := f.build(prune.Options{})

	if len(p.Delete) != 0 {
		t.Fatalf("Delete = %v, want nothing — the copies differ", deleted(p))
	}
	if r := keptReason(t, p, "DSC_0001.NEF"); r != prune.ReasonMismatch {
		t.Errorf("kept for %q, want %q", r, prune.ReasonMismatch)
	}
	if p.Mismatches != 1 {
		t.Errorf("Mismatches = %d, want 1", p.Mismatches)
	}
}

func TestBuild_DifferentSizeIsNotHashed(t *testing.T) {
	f := newFixture(t)
	f.put(f.cfg.SSDPhotos, "DSC_0001.NEF", "aaaa", shot)
	f.put(f.cfg.NASPhotos, "DSC_0001.NEF", "aa", shot)

	p := f.build(prune.Options{})

	if len(p.Delete) != 0 {
		t.Fatalf("Delete = %v, want nothing", deleted(p))
	}
	if r := keptReason(t, p, "DSC_0001.NEF"); r != prune.ReasonSize {
		t.Errorf("kept for %q, want %q", r, prune.ReasonSize)
	}
	if p.Mismatches != 0 {
		t.Errorf("Mismatches = %d — a size difference is not a damaged copy", p.Mismatches)
	}
}

// Category comes from the extension, so a video is compared against the NAS
// videos root even though it was found by the same scan.
func TestBuild_ComparesAgainstTheCategoryRoot(t *testing.T) {
	f := newFixture(t)
	f.put(f.cfg.SSDVideos, "VID_0001.MOV", "movie", shot)
	f.put(f.cfg.NASVideos, "VID_0001.MOV", "movie", shot)
	// The same name under the photos root must not count.
	f.put(f.cfg.SSDPhotos, "DSC_0001.NEF", "photo", shot)
	f.put(f.cfg.NASVideos, "DSC_0001.NEF", "photo", shot)

	p := f.build(prune.Options{})

	if got := deleted(p); len(got) != 1 || got[0] != "VID_0001.MOV" {
		t.Fatalf("Delete = %v, want only the video", got)
	}
	if r := keptReason(t, p, "DSC_0001.NEF"); r != prune.ReasonMissing {
		t.Errorf("kept for %q, want %q", r, prune.ReasonMissing)
	}
}

// An unmounted NAS root means nothing can be compared, so nothing goes.
func TestBuild_UnmountedNASRootKeepsEverything(t *testing.T) {
	f := newFixture(t)
	f.cfg.NASPhotos = filepath.Join(f.dir, "unmounted", "photos")
	f.put(f.cfg.SSDPhotos, "DSC_0001.NEF", "one", shot)

	p := f.build(prune.Options{})

	if len(p.Delete) != 0 {
		t.Fatalf("Delete = %v, want nothing with the NAS root unmounted", deleted(p))
	}
	if r := keptReason(t, p, "DSC_0001.NEF"); r != prune.ReasonRootUnavailable {
		t.Errorf("kept for %q, want %q", r, prune.ReasonRootUnavailable)
	}
}

// --older-than keeps a recent shoot on the fast disk even though the NAS has it.
func TestBuild_OlderThanKeepsRecentWork(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.Local)
	old := now.AddDate(0, 0, -30)
	recent := now.AddDate(0, 0, -2)
	f.put(f.cfg.SSDPhotos, "OLD.NEF", "old", old)
	f.put(f.cfg.NASPhotos, "OLD.NEF", "old", old)
	f.put(f.cfg.SSDPhotos, "NEW.NEF", "new", recent)
	f.put(f.cfg.NASPhotos, "NEW.NEF", "new", recent)

	p := f.build(prune.Options{OlderThan: 14 * 24 * time.Hour, Now: now})

	if got := deleted(p); len(got) != 1 || got[0] != "OLD.NEF" {
		t.Fatalf("Delete = %v, want only the old file", got)
	}
	if r := keptReason(t, p, "NEW.NEF"); r != prune.ReasonTooRecent {
		t.Errorf("kept for %q, want %q", r, prune.ReasonTooRecent)
	}
}

// Apply deletes exactly the planned files, leaves the NAS alone, and clears
// the date directories it empties without touching the root.
func TestApply_DeletesAndTidies(t *testing.T) {
	f := newFixture(t)
	ssd := f.put(f.cfg.SSDPhotos, "DSC_0001.NEF", "one", shot)
	nas := f.put(f.cfg.NASPhotos, "DSC_0001.NEF", "one", shot)
	keep := f.put(f.cfg.SSDPhotos, "DSC_0002.NEF", "two", shot)

	p := f.build(prune.Options{})
	n, freed, errs := prune.Apply(f.cfg, p, log.New(io.Discard, "", 0))

	if len(errs) != 0 {
		t.Fatalf("Apply reported %v", errs)
	}
	if n != 1 || freed != 3 {
		t.Errorf("deleted %d files freeing %d bytes, want 1 and 3", n, freed)
	}
	if _, err := os.Stat(ssd); !os.IsNotExist(err) {
		t.Error("the verified SSD file is still there")
	}
	if _, err := os.Stat(nas); err != nil {
		t.Error("the NAS copy was touched")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("an unverified file was deleted")
	}
	// The day directory still holds DSC_0002.NEF, so it must survive.
	if _, err := os.Stat(filepath.Dir(ssd)); err != nil {
		t.Error("a non-empty directory was removed")
	}
}

func TestApply_RemovesEmptiedDirectoriesButNotTheRoot(t *testing.T) {
	f := newFixture(t)
	ssd := f.put(f.cfg.SSDPhotos, "DSC_0001.NEF", "one", shot)
	f.put(f.cfg.NASPhotos, "DSC_0001.NEF", "one", shot)

	p := f.build(prune.Options{})
	if _, _, errs := prune.Apply(f.cfg, p, log.New(io.Discard, "", 0)); len(errs) != 0 {
		t.Fatalf("Apply reported %v", errs)
	}

	for dir := filepath.Dir(ssd); dir != f.cfg.SSDPhotos; dir = filepath.Dir(dir) {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s survived although it is empty", dir)
		}
	}
	if _, err := os.Stat(f.cfg.SSDPhotos); err != nil {
		t.Error("the SSD root itself was removed")
	}
}

// Without somewhere else holding the files, pruning is just deleting.
func TestBuild_RefusesWithoutBothDevices(t *testing.T) {
	f := newFixture(t)
	f.cfg.NASPhotos, f.cfg.NASVideos = "", ""
	if _, err := prune.Build(f.cfg, log.New(io.Discard, "", 0), prune.Options{}, nil); err == nil {
		t.Error("Build accepted a config with no NAS")
	}

	f2 := newFixture(t)
	f2.cfg.SSDPhotos, f2.cfg.SSDVideos = "", ""
	if _, err := prune.Build(f2.cfg, log.New(io.Discard, "", 0), prune.Options{}, nil); err == nil {
		t.Error("Build accepted a config with no SSD")
	}
}

// A merged SSD root is scanned once; its files must not be considered twice.
func TestBuild_MergedSSDRoot(t *testing.T) {
	f := newFixture(t)
	merged := filepath.Join(f.dir, "ssd", "all")
	f.cfg.SSDPhotos, f.cfg.SSDVideos = merged, merged
	f.put(merged, "DSC_0001.NEF", "one", shot)
	f.put(f.cfg.NASPhotos, "DSC_0001.NEF", "one", shot)

	p := f.build(prune.Options{})

	if len(p.Delete) != 1 {
		t.Fatalf("Delete = %v, want one entry", deleted(p))
	}
	if p.Bytes != 3 {
		t.Errorf("Bytes = %d, want 3 — the merged root was counted twice", p.Bytes)
	}
}
