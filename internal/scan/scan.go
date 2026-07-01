package scan

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
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
	Size    int64
	// ModTime is used to compute the date component on the destination.
	ModTime time.Time
}

// Key returns a canonical lookup key for the source (lowercased RelPath).
func (f FileInfo) Key() string {
	return strings.ToLower(f.RelPath)
}

// DestRelPath returns the expected relative path on a destination root.
// category is "photos" or "videos". Structure is year/year-month/year-month-day:
//
//	"photos/2026/2026-03/2026-03-24/DSC_0001.NEF"
//	"videos/2026/2026-03/2026-03-24/VIDEO001.MOV"
//
// Always uses forward slashes so keys are consistent across platforms.
func (f FileInfo) DestRelPath(category string) string {
	year  := f.ModTime.Format("2006")
	month := f.ModTime.Format("2006-01")
	day   := f.ModTime.Format("2006-01-02")
	return path.Join(category, year, month, day, filepath.Base(f.RelPath))
}

// DestKey returns a lowercased DestRelPath for map lookups.
func (f FileInfo) DestKey(category string) string {
	return strings.ToLower(f.DestRelPath(category))
}

// Walk scans root recursively and returns files whose extension (case-insensitive) is in exts.
// exts must already be lowercase (use config.NormalisedExtensions()).
// Permission errors on subdirectories are silently skipped.
func Walk(root string, exts []string) ([]FileInfo, error) {
	extSet := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		extSet[e] = struct{}{}
	}

	var files []FileInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs/files
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if _, ok := extSet[ext]; !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, FileInfo{
			RelPath: filepath.ToSlash(rel),
			AbsPath: path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	return files, err
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
// categoryFn maps a FileInfo to "photos" or "videos".
// dstIndex must be keyed by lowercased RelPath (category/date/filename).
//
// A name collision (same name, different size) is resolved at copy time by
// saving the file with a _N suffix. On later runs the original key still
// mismatches by size, so the _N variants are probed too — otherwise the same
// file would be copied again on every run, creating _2, _3, … forever.
func MissingFromDest(src []FileInfo, dstIndex map[string]FileInfo, categoryFn func(FileInfo) string) []FileInfo {
	var out []FileInfo
	for _, f := range src {
		key := f.DestKey(categoryFn(f))
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
func MissingByRelPath(src []FileInfo, dstIndex map[string]FileInfo) []FileInfo {
	var out []FileInfo
	for _, f := range src {
		existing, found := dstIndex[strings.ToLower(f.RelPath)]
		if !found || existing.Size != f.Size {
			out = append(out, f)
		}
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
