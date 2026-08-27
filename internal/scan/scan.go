package scan

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileInfo represents a discovered file.
type FileInfo struct {
	// RelPath is the path relative to the scanned root (e.g. "DCIM/100NIKON/DSC_0001.NEF").
	RelPath string
	// AbsPath is the full absolute path.
	AbsPath string
	// Size in bytes.
	Size int64
	// ModTime is the filesystem modification time. It is what decides whether a
	// file is still being written (SplitStable) and what the destination copy is
	// stamped with — not, on its own, where the file is filed.
	ModTime time.Time
	// CaptureTime is when the camera recorded the file, read from EXIF or the
	// video header by CaptureTime(). Zero when the file carries no such
	// metadata; use DateTaken() rather than reading this directly.
	CaptureTime time.Time
}

// Key returns a canonical lookup key for the source (lowercased RelPath).
func (f FileInfo) Key() string {
	return strings.ToLower(f.RelPath)
}

// DateTaken returns the date the file should be filed under: the camera's
// capture time when the file carries one, otherwise the filesystem modtime.
//
// The modtime alone is not enough — copying a card to an external drive with a
// file manager stamps every file with "now", which would bury a whole shoot
// under the date it was copied.
func (f FileInfo) DateTaken() time.Time {
	if !f.CaptureTime.IsZero() {
		return f.CaptureTime
	}
	return f.ModTime
}

// DestRelPath returns the expected relative path under a destination root.
// The category (photos/videos) is expressed by *which* root the file goes to,
// so the path itself is date-only:
//
//	"2026/2026-03/2026-03-24/DSC_0001.NEF"
//
// Always uses forward slashes so keys are consistent across platforms.
func (f FileInfo) DestRelPath() string {
	taken := f.DateTaken()
	year := taken.Format("2006")
	month := taken.Format("2006-01")
	day := taken.Format("2006-01-02")
	return path.Join(year, month, day, filepath.Base(f.RelPath))
}

// DestKey returns a lowercased DestRelPath for map lookups.
func (f FileInfo) DestKey() string {
	return strings.ToLower(f.DestRelPath())
}

// Unreadable records one path a scan could not read, and why.
//
// A directory that cannot be opened is not an empty directory: its files are
// simply invisible to the walk. Dropping it silently is what turned a card
// with an I/O error into a run where status reported nothing missing, copy
// reported every file copied and verify reported every file OK — while a
// photograph nobody had ever read sat on the card waiting to be formatted
// away. Every scan therefore hands these back, and callers must say so.
type Unreadable struct {
	// Path is the file or directory that could not be read.
	Path string
	// Err is what the filesystem said.
	Err error
}

func (u Unreadable) String() string {
	return fmt.Sprintf("%s: %v", u.Path, u.Err)
}

// Paths returns just the paths of a set of unreadable entries, for messages
// that name them without repeating each error.
func Paths(u []Unreadable) []string {
	out := make([]string, len(u))
	for i, e := range u {
		out[i] = e.Path
	}
	return out
}

// Walk scans root recursively and returns files whose extension
// (case-insensitive) is in exts. exts must already be lowercase (use
// config.NormalisedExtensions()).
//
// Paths the walk could not read are collected into the second return value
// rather than dropped, and the walk carries on so the rest of the device is
// still scanned. A caller that ignores that list is reporting on a device it
// only partly saw.
func Walk(root string, exts []string) ([]FileInfo, []Unreadable, error) {
	w := &walker{extSet: make(map[string]struct{}, len(exts)), root: root}
	for _, e := range exts {
		w.extSet[e] = struct{}{}
	}
	err := filepath.WalkDir(root, w.visit)
	return w.files, w.unreadable, err
}

// walker accumulates one Walk. It exists so visit can be driven directly by a
// test with synthesised failures: a readdir error is the case that matters
// most here and the hardest one to arrange on a real filesystem.
type walker struct {
	root       string
	extSet     map[string]struct{}
	files      []FileInfo
	unreadable []Unreadable
}

// visit is the filepath.WalkDir callback. Errors are recorded and the walk
// continues; only a path that cannot be made relative to the root aborts it,
// since that means the walk is somewhere it was never asked to go.
func (w *walker) visit(path string, d fs.DirEntry, err error) error {
	if err != nil {
		w.unreadable = append(w.unreadable, Unreadable{Path: path, Err: err})
		return nil
	}
	if d.IsDir() {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(d.Name()))
	if _, ok := w.extSet[ext]; !ok {
		return nil
	}
	info, err := d.Info()
	if err != nil {
		// The entry was listed but its metadata could not be read — the file
		// is real and this scan cannot describe it.
		w.unreadable = append(w.unreadable, Unreadable{Path: path, Err: err})
		return nil
	}
	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return err
	}
	w.files = append(w.files, FileInfo{
		RelPath: filepath.ToSlash(rel),
		AbsPath: path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	})
	return nil
}

// StableAge is the minimum age of a source file's modtime before it is
// considered fully written and safe to copy.
const StableAge = 10 * time.Second

// SplitStable partitions files into those safe to copy and those whose
// modtime is within minAge of now — i.e. probably still being written to the
// card. Copying a file mid-write risks a truncated destination, and once the
// writer restores the real timestamp the file would be copied again under a
// second date directory. Modtimes far in the future (e.g. a camera clock set
// wrong) are treated as stable — such files are not being written right now.
func SplitStable(files []FileInfo, now time.Time, minAge time.Duration) (stable, unstable []FileInfo) {
	for _, f := range files {
		age := now.Sub(f.ModTime)
		if age < minAge && age > -minAge {
			unstable = append(unstable, f)
		} else {
			stable = append(stable, f)
		}
	}
	return stable, unstable
}

// SplitByCategory partitions files into photos and videos using categoryFn
// (which normally wraps config.Category, i.e. classification by extension).
func SplitByCategory(files []FileInfo, categoryFn func(FileInfo) string) (photos, videos []FileInfo) {
	for _, f := range files {
		if categoryFn(f) == "videos" {
			videos = append(videos, f)
		} else {
			photos = append(photos, f)
		}
	}
	return photos, videos
}

// WalkDual scans a device's photos and videos roots. When both categories
// share one root it is scanned once and the same list is returned for both.
// A missing or empty root yields a nil list.
//
// Unlike a source scan this discards what it could not read: these are
// destination trees, and an unreadable corner of one makes files look missing
// rather than making them disappear — the copy that follows adds a duplicate
// instead of skipping a photograph.
func WalkDual(photosRoot, videosRoot string, exts []string) (photos, videos []FileInfo) {
	if photosRoot != "" {
		photos, _, _ = Walk(photosRoot, exts)
	}
	if videosRoot == photosRoot {
		return photos, photos
	}
	if videosRoot != "" {
		videos, _, _ = Walk(videosRoot, exts)
	}
	return photos, videos
}

// IndexByRelPath indexes files by their lowercased RelPath.
// Used for destination directories whose RelPath already includes category/date.
func IndexByRelPath(files []FileInfo) map[string]FileInfo {
	m := make(map[string]FileInfo, len(files))
	for _, f := range files {
		m[strings.ToLower(f.RelPath)] = f
	}
	return m
}

// IndexByKey indexes files by their source Key() (lowercased source RelPath).
func IndexByKey(files []FileInfo) map[string]FileInfo {
	m := make(map[string]FileInfo, len(files))
	for _, f := range files {
		m[f.Key()] = f
	}
	return m
}

// MissingFromDest returns camera files not yet on the destination.
// dstIndex must be keyed by lowercased date-based RelPath and belong to the
// same category root as the src files — split mixed sources by category first.
//
// A name collision (same name, different size) is resolved at copy time by
// saving the file with a _N suffix. On later runs the original key still
// mismatches by size, so the _N variants are probed too — otherwise the same
// file would be copied again on every run, creating _2, _3, … forever.
//
// A source filed under a date the destination does not have is looked up a
// second time by basename+size across the whole tree, so a copy that sits
// somewhere else is not duplicated: backups made before capture times were read
// sit under their modtime's date, and a file with no capture metadata still
// moves date when something rewrites its modtime.
//
// Basename+size alone is a weak identity — three cards all number their first
// frame DSC_0001, and two of those can share a byte-exact size. Such a match is
// therefore confirmed against the capture time of the destination file before
// the source is skipped; a different capture time means a different photograph
// and the source is copied (landing as a _N variant). Files with no capture
// time on either side fall back to trusting basename+size, as before.
func MissingFromDest(src []FileInfo, dstIndex map[string]FileInfo) []FileInfo {
	byNameSize := IndexByNameSize(dstIndex)

	// Pass 1: decide everything that needs no metadata, and note the sources
	// whose only evidence is a basename+size twin under another date.
	type candidate struct {
		srcIdx int
		twin   FileInfo
	}
	var out []FileInfo
	var candidates []candidate
	for i, f := range src {
		key := f.DestKey()
		existing, found := dstIndex[key]
		if found && existing.Size == f.Size {
			continue
		}
		if found && collisionCopyExists(key, f.Size, dstIndex) {
			continue
		}
		twin, ok := byNameSize[NameSizeKey(f.RelPath, f.Size)]
		if !ok {
			out = append(out, f)
			continue
		}
		if f.CaptureTime.IsZero() {
			// No capture time to compare — basename+size is all there is.
			continue
		}
		candidates = append(candidates, candidate{srcIdx: i, twin: twin})
	}
	if len(candidates) == 0 {
		return out
	}

	// Pass 2: read the twins' capture times concurrently. Only the ambiguous
	// files are read, so a destination whose layout already matches costs
	// nothing here.
	paths := make([]string, len(candidates))
	for i, c := range candidates {
		paths[i] = c.twin.AbsPath
	}
	times := captureTimesOf(paths)

	// Re-merge in source order so the returned batch keeps its input ordering.
	var confirmed []FileInfo
	for i, c := range candidates {
		if t, ok := times[i]; ok && !t.Equal(src[c.srcIdx].CaptureTime) {
			confirmed = append(confirmed, src[c.srcIdx])
		}
	}
	if len(confirmed) == 0 {
		return out
	}
	out = append(out, confirmed...)
	sortBySrcOrder(out, src)
	return out
}

// sortBySrcOrder restores src's ordering in out, which the two-pass split above
// breaks. Callers rely on the batch order (videos first, size-asc) being applied
// to a list that still reflects the scan.
func sortBySrcOrder(out []FileInfo, src []FileInfo) {
	pos := make(map[string]int, len(src))
	for i, f := range src {
		pos[f.AbsPath] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return pos[out[i].AbsPath] < pos[out[j].AbsPath] })
}

// NameSizeKey identifies a file by lowercased basename and size, independent
// of which date directory it sits in.
func NameSizeKey(relPath string, size int64) string {
	return fmt.Sprintf("%s|%d", strings.ToLower(path.Base(relPath)), size)
}

// IndexByNameSize re-keys a destination index by NameSizeKey, so a file can be
// found regardless of which date directory it ended up in. Used to recognise
// files copied before capture-time filing existed, and files whose source
// modtime changed after they were copied.
func IndexByNameSize(dstIndex map[string]FileInfo) map[string]FileInfo {
	m := make(map[string]FileInfo, len(dstIndex))
	for _, d := range dstIndex {
		m[NameSizeKey(d.RelPath, d.Size)] = d
	}
	return m
}

// collisionCopyExists reports whether a _N collision variant of key exists in
// dstIndex with the wanted size. Variants are allocated contiguously by
// safeCreate, so probing stops at the first missing suffix.
func collisionCopyExists(key string, size int64, dstIndex map[string]FileInfo) bool {
	ext := filepath.Ext(key)
	stem := strings.TrimSuffix(key, ext)
	for i := 1; ; i++ {
		variant, found := dstIndex[fmt.Sprintf("%s_%d%s", stem, i, ext)]
		if !found {
			return false
		}
		if variant.Size == size {
			return true
		}
	}
}

// MissingByRelPath returns files from src absent in dstIndex (keyed by lowercased RelPath).
// Used for SSD→NAS where both sides share the same category/date/filename structure.
//
// Like MissingFromDest, a size mismatch under the original name is resolved by
// probing the _N collision variants — otherwise a stray destination file (e.g.
// a partial copy left behind by a dropped connection) would cause the source
// to be recopied as _2, _3, … on every run.
func MissingByRelPath(src []FileInfo, dstIndex map[string]FileInfo) []FileInfo {
	var out []FileInfo
	for _, f := range src {
		key := strings.ToLower(f.RelPath)
		existing, found := dstIndex[key]
		if found && existing.Size == f.Size {
			continue
		}
		if found && collisionCopyExists(key, f.Size, dstIndex) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// FilterByExts returns only files matching one of exts (already lowercased).
func FilterByExts(files []FileInfo, exts []string) []FileInfo {
	if len(exts) == 0 {
		return files
	}
	extSet := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		extSet[e] = struct{}{}
	}
	var out []FileInfo
	for _, f := range files {
		if _, ok := extSet[strings.ToLower(filepath.Ext(f.RelPath))]; ok {
			out = append(out, f)
		}
	}
	return out
}
