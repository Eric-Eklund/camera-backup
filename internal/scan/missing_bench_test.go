package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildTree writes n JPEGs carrying capture dates into a date tree under dir and
// returns their FileInfos with capture times filled, the way a source scan
// produces them.
func buildTree(tb testing.TB, dir string, n int, destLayout bool) []FileInfo {
	tb.Helper()
	base := time.Date(2026, 1, 1, 8, 0, 0, 0, time.Local)
	files := make([]FileInfo, 0, n)
	for i := 0; i < n; i++ {
		// 30 files per day, so the tree has a realistic number of day folders.
		shot := base.AddDate(0, 0, i/30).Add(time.Duration(i%30) * time.Minute)
		name := fmt.Sprintf("DSC_%04d.JPG", i%1000)

		var rel string
		if destLayout {
			rel = filepath.Join(shot.Format("2006"), shot.Format("2006-01"),
				shot.Format("2006-01-02"), name)
		} else {
			rel = filepath.Join("DCIM", fmt.Sprintf("%03dNIKON", i/500), name)
		}
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			tb.Fatal(err)
		}
		data := buildJPEG(tb, shot.Format("2006:01:02 15:04:05"), false)
		// Unique size per file, as real photos have.
		data = append(data, make([]byte, 2048+i)...)
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			tb.Fatal(err)
		}
		files = append(files, FileInfo{
			RelPath: filepath.ToSlash(rel), AbsPath: abs,
			Size: int64(len(data)), ModTime: shot, CaptureTime: shot,
		})
	}
	return files
}

const benchN = 3000

// BenchmarkMissingFromDest_SteadyState is the normal case: every source file
// already sits at the date path it computes, so the comparison never needs to
// read metadata from the destination.
func BenchmarkMissingFromDest_SteadyState(b *testing.B) {
	dir := b.TempDir()
	src := buildTree(b, filepath.Join(dir, "card"), benchN, false)
	dst := buildTree(b, filepath.Join(dir, "nas"), benchN, true)
	idx := IndexByRelPath(dst)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := MissingFromDest(src, idx); len(got) != 0 {
			b.Fatalf("expected nothing missing, got %d", len(got))
		}
	}
}

// BenchmarkMissingFromDest_Migration is the worst case: the destination holds
// every file under a single wrong date (a backup made before capture times were
// read), so every source file needs its twin's capture time read to confirm it.
func BenchmarkMissingFromDest_Migration(b *testing.B) {
	dir := b.TempDir()
	src := buildTree(b, filepath.Join(dir, "card"), benchN, false)

	// Re-file the destination copies under one "copy date" folder.
	nas := filepath.Join(dir, "nas")
	dst := make([]FileInfo, 0, len(src))
	for i, f := range src {
		rel := filepath.Join("2026", "2026-07", "2026-07-28",
			fmt.Sprintf("DSC_%04d.JPG", i))
		abs := filepath.Join(nas, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			b.Fatal(err)
		}
		data, err := os.ReadFile(f.AbsPath)
		if err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			b.Fatal(err)
		}
		dst = append(dst, FileInfo{
			RelPath: filepath.ToSlash(rel), AbsPath: abs, Size: f.Size, ModTime: f.ModTime,
		})
	}
	// Give the destination the same basenames as the source so basename+size
	// matches and the confirmation read is what decides.
	for i := range dst {
		dst[i].RelPath = filepath.ToSlash(filepath.Join("2026", "2026-07", "2026-07-28",
			filepath.Base(src[i].RelPath)))
		newAbs := filepath.Join(nas, dst[i].RelPath)
		if newAbs != dst[i].AbsPath {
			if err := os.Rename(dst[i].AbsPath, newAbs); err == nil {
				dst[i].AbsPath = newAbs
			}
		}
	}
	idx := IndexByRelPath(dst)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		MissingFromDest(src, idx)
	}
}

// BenchmarkWalkSource measures the capture-time read that every source scan now
// does, which is the part that scales with card size.
func BenchmarkWalkSource(b *testing.B) {
	dir := b.TempDir()
	buildTree(b, dir, benchN, false)
	exts := []string{".jpg"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		files, err := WalkSource(dir, exts)
		if err != nil {
			b.Fatal(err)
		}
		if len(files) != benchN {
			b.Fatalf("got %d files, want %d", len(files), benchN)
		}
	}
}

// BenchmarkWalk is the same scan without reading any metadata, for comparison.
func BenchmarkWalk(b *testing.B) {
	dir := b.TempDir()
	buildTree(b, dir, benchN, false)
	exts := []string{".jpg"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Walk(dir, exts); err != nil {
			b.Fatal(err)
		}
	}
}
