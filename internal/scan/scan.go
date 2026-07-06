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
	Size int64
	// ModTime is used to compute the date component on the destination.
	ModTime time.Time
}

// Key returns a canonical lookup key for the source (lowercased RelPath).
func (f FileInfo) Key() string {
	return strings.ToLower(f.RelPath)
}

// DestRelPath returns the expected relative path under a destination root.
// The category (photos/videos) is expressed by *which* root the file goes to,
// so the path itself is date-only:
//
//	"2026/2026-03/2026-03-24/DSC_0001.NEF"
//
// Always uses forward slashes so keys are consistent across platforms.
func (f FileInfo) DestRelPath() string {
	year := f.ModTime.Format("2006")
	month := f.ModTime.Format("2006-01")
	day := f.ModTime.Format("2006-01-02")
	return path.Join(year, month, day, filepath.Base(f.RelPath))
}

// DestKey returns a lowercased DestRelPath for map lookups.
func (f FileInfo) DestKey() string {
	return strings.ToLower(f.DestRelPath())
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
func WalkDual(photosRoot, videosRoot string, exts []string) (photos, videos []FileInfo) {
	if photosRoot != "" {
		photos, _ = Walk(photosRoot, exts)
	}
	if videosRoot == photosRoot {
		return photos, photos
	}
	if videosRoot != "" {
		videos, _ = Walk(videosRoot, exts)
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
func MissingFromDest(src []FileInfo, dstIndex map[string]FileInfo) []FileInfo {
	var out []FileInfo
	for _, f := range src {
		key := f.DestKey()
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
