// Package prune removes files from the local SSD once the NAS holds a copy
// that has been proven identical.
//
// Every other operation in this tool only ever adds files. This one deletes,
// which is why it is deliberately narrow: it reads both copies in full and
// compares SHA256 hashes, it only ever touches the SSD, and it does nothing at
// all unless the caller asks for the deletion explicitly. Freeing space on the
// staging disk is the last step of the workflow the rest of the tool describes
// — camera to SSD, SSD to NAS, verify — and doing it by hand means picking
// directories to delete while trusting that the copies made it.
package prune

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/checksum"
	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
)

// Reason says why a file was kept. An empty Reason marks a file that can go.
type Reason string

const (
	// ReasonMissing means the NAS has no file at the matching path.
	ReasonMissing Reason = "not on the NAS"
	// ReasonSize means a file is there but a different size — never hashed,
	// because the sizes alone already say the copies differ.
	ReasonSize Reason = "differs in size from the NAS copy"
	// ReasonMismatch means both files are the same size and hash differently.
	// This is the loud one: one of the two copies is damaged.
	ReasonMismatch Reason = "SHA256 differs from the NAS copy"
	// ReasonTooRecent means the file is newer than the --older-than cutoff.
	ReasonTooRecent Reason = "newer than the cutoff"
	// ReasonRootUnavailable means the file's NAS category root is not mounted,
	// so nothing could be compared.
	ReasonRootUnavailable Reason = "NAS root not mounted"
)

// Candidate is one file on the SSD and what was decided about it.
type Candidate struct {
	File    scan.FileInfo
	NASPath string
	Reason  Reason // empty when the file is safe to delete
}

// Plan is the outcome of a comparison pass: what can go, what stays and why.
type Plan struct {
	// Delete holds files whose NAS copy hashed identical, oldest first.
	Delete []Candidate
	// Keep holds everything else, with the reason it was kept.
	Keep []Candidate
	// Bytes is how much deleting the plan would free.
	Bytes int64
	// Mismatches counts kept files whose copies differ despite equal sizes —
	// the number worth reading twice, since it means a damaged copy.
	Mismatches int
}

// ProgressFn is called once per file as the comparison proceeds.
type ProgressFn func(done, total int, path string)

// Options bound what a pass considers.
type Options struct {
	// OlderThan keeps files whose capture date is within this duration of now,
	// so a recent shoot stays on the fast disk. Zero considers everything.
	OlderThan time.Duration
	// Now is the clock the cutoff is measured from; zero means time.Now.
	Now time.Time
}

// Build compares every file on the SSD against its NAS counterpart and returns
// what could be deleted. Nothing is written or removed.
//
// A file is only ever proposed for deletion on the strength of a full SHA256
// of both copies, read in this pass. Sizes are compared first because a
// difference there settles the question without reading either file.
func Build(cfg *config.Config, logger *log.Logger, opt Options, fn ProgressFn) (*Plan, error) {
	if !cfg.SSDConfigured() {
		return nil, fmt.Errorf("prune: no SSD roots configured — there is nothing to prune")
	}
	if !cfg.NASConfigured() {
		return nil, fmt.Errorf("prune: no NAS roots configured — a copy has to exist somewhere else first")
	}

	exts := cfg.NormalisedExtensions()
	ssdPhotos, ssdVideos := scan.WalkDual(cfg.SSDPhotos, cfg.SSDVideos, exts)

	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	var cutoff time.Time
	if opt.OlderThan > 0 {
		cutoff = now.Add(-opt.OlderThan)
	}

	// A merged SSD returns the same scan twice; the category of each file is
	// decided by its extension either way.
	files := ssdPhotos
	if !cfg.SSDMerged() {
		files = append(append([]scan.FileInfo{}, ssdPhotos...), ssdVideos...)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })

	// Destination scans do not read capture times — comparing by relative path
	// never needs them. This pass does: "older than two weeks" has to mean the
	// day the shutter fired, not the day the file happened to be written, and
	// on a staging copy those differ whenever the camera's own date was used
	// to file it. A header read per file is nothing beside hashing them.
	scan.FillCaptureTimes(files)

	plan := &Plan{}
	for i, f := range files {
		if fn != nil {
			fn(i+1, len(files), f.AbsPath)
		}
		cat := cfg.Category(f.RelPath)
		nasRoot := cfg.NASRoot(cat)
		c := Candidate{File: f}

		if !config.RootAvailable(nasRoot) {
			c.Reason = ReasonRootUnavailable
			plan.Keep = append(plan.Keep, c)
			continue
		}
		c.NASPath = filepath.Join(nasRoot, f.RelPath)

		if !cutoff.IsZero() && f.DateTaken().After(cutoff) {
			c.Reason = ReasonTooRecent
			plan.Keep = append(plan.Keep, c)
			continue
		}

		st, err := os.Stat(c.NASPath)
		if err != nil || st.IsDir() {
			c.Reason = ReasonMissing
			plan.Keep = append(plan.Keep, c)
			continue
		}
		if st.Size() != f.Size {
			c.Reason = ReasonSize
			plan.Keep = append(plan.Keep, c)
			continue
		}

		same, err := sameContent(f.AbsPath, c.NASPath)
		if err != nil {
			// An unreadable copy is not a verified copy.
			logger.Printf("prune: %s: %v", f.RelPath, err)
			c.Reason = ReasonMismatch
			plan.Keep = append(plan.Keep, c)
			plan.Mismatches++
			continue
		}
		if !same {
			logger.Printf("prune: SHA256 mismatch, keeping %s", f.AbsPath)
			c.Reason = ReasonMismatch
			plan.Keep = append(plan.Keep, c)
			plan.Mismatches++
			continue
		}

		plan.Delete = append(plan.Delete, c)
		plan.Bytes += f.Size
	}

	sort.Slice(plan.Delete, func(i, j int) bool {
		return plan.Delete[i].File.DateTaken().Before(plan.Delete[j].File.DateTaken())
	})
	logger.Printf("prune: %d file(s) verified against the NAS, %d kept (%d mismatching)",
		len(plan.Delete), len(plan.Keep), plan.Mismatches)
	return plan, nil
}

// sameContent hashes both copies in full and reports whether they match.
func sameContent(a, b string) (bool, error) {
	sumA, err := checksum.File(a)
	if err != nil {
		return false, err
	}
	sumB, err := checksum.File(b)
	if err != nil {
		return false, err
	}
	return sumA == sumB, nil
}

// Apply deletes the files in the plan and removes the directories that are
// left empty behind them. It re-checks nothing: the plan is the decision, and
// it is only ever built from hashes read moments earlier.
//
// Deletion failures are collected rather than fatal — one file held open by
// another program should not stop the rest.
func Apply(cfg *config.Config, plan *Plan, logger *log.Logger) (deleted int, freed int64, errs []error) {
	dirs := map[string]bool{}
	for _, c := range plan.Delete {
		if err := os.Remove(c.File.AbsPath); err != nil {
			errs = append(errs, err)
			logger.Printf("prune: cannot delete %s: %v", c.File.AbsPath, err)
			continue
		}
		deleted++
		freed += c.File.Size
		dirs[filepath.Dir(c.File.AbsPath)] = true
		logger.Printf("prune: deleted %s (NAS copy verified at %s)", c.File.AbsPath, c.NASPath)
	}
	removeEmptyDirs(dirs, cfg.SSDPhotos, cfg.SSDVideos)
	return deleted, freed, errs
}

// removeEmptyDirs clears out the date directories a prune empties, walking up
// from each one. The roots themselves are left in place — they are mount
// points or configured destinations, not something this created.
func removeEmptyDirs(dirs map[string]bool, roots ...string) {
	clean := make([]string, 0, len(roots))
	for _, r := range roots {
		if r != "" {
			if abs, err := filepath.Abs(r); err == nil {
				clean = append(clean, filepath.Clean(abs))
			}
		}
	}

	for dir := range dirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		for cur := filepath.Clean(abs); under(cur, clean); cur = filepath.Dir(cur) {
			entries, err := os.ReadDir(cur)
			if err != nil || len(entries) > 0 {
				break
			}
			if err := os.Remove(cur); err != nil {
				break
			}
		}
	}
}

// under reports whether dir sits strictly below one of the roots. A root is
// never "under" itself, which is what stops the walk before it removes a
// configured destination.
func under(dir string, roots []string) bool {
	for _, root := range roots {
		if dir == root {
			return false
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			continue
		}
		if rel != "." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
}
