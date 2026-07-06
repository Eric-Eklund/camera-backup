package scan_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/scan"
)

// ── Walk ─────────────────────────────────────────────────────────────────────

func TestWalk_FindsMatchingExtensions(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "DCIM/100NIKON"), 0755)
	touch(t, filepath.Join(dir, "DCIM/100NIKON/DSC_0001.NEF"))
	touch(t, filepath.Join(dir, "DCIM/100NIKON/DSC_0002.JPG"))
	touch(t, filepath.Join(dir, "DCIM/100NIKON/DSC_0003.TXT")) // excluded

	files, err := scan.Walk(dir, []string{".nef", ".jpg"})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("len = %d, want 2", len(files))
	}
}

func TestWalk_CaseInsensitiveExtensions(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "upper.NEF"))
	touch(t, filepath.Join(dir, "lower.nef"))
	touch(t, filepath.Join(dir, "mixed.Nef"))

	files, err := scan.Walk(dir, []string{".nef"})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("len = %d, want 3", len(files))
	}
}

func TestWalk_EmptyDir(t *testing.T) {
	files, err := scan.Walk(t.TempDir(), []string{".nef"})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("len = %d, want 0", len(files))
	}
}

func TestWalk_RecursiveSubdirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a/b/c"), 0755)
	touch(t, filepath.Join(dir, "root.NEF"))
	touch(t, filepath.Join(dir, "a/mid.NEF"))
	touch(t, filepath.Join(dir, "a/b/c/deep.NEF"))

	files, err := scan.Walk(dir, []string{".nef"})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("len = %d, want 3", len(files))
	}
}

// ── DestRelPath / DestKey ─────────────────────────────────────────────────────

func TestDestRelPath(t *testing.T) {
	f := scan.FileInfo{
		RelPath: "DCIM/100NIKON/DSC_0001.NEF",
		ModTime: time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC),
	}
	got := f.DestRelPath()
	want := "2026/2026-03/2026-03-25/DSC_0001.NEF"
	if got != want {
		t.Errorf("DestRelPath = %q, want %q", got, want)
	}
}

func TestDestKey_IsLowercase(t *testing.T) {
	f := scan.FileInfo{
		RelPath: "DCIM/DSC_0001.NEF",
		ModTime: time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC),
	}
	key := f.DestKey()
	for _, c := range key {
		if c >= 'A' && c <= 'Z' {
			t.Errorf("DestKey contains uppercase: %q", key)
			break
		}
	}
}

// ── MissingFromDest ───────────────────────────────────────────────────────────

func TestMissingFromDest_NewFile(t *testing.T) {
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	src := []scan.FileInfo{fi("DCIM/DSC_0001.NEF", 1024, modtime)}

	missing := scan.MissingFromDest(src, map[string]scan.FileInfo{})
	if len(missing) != 1 {
		t.Errorf("len = %d, want 1", len(missing))
	}
}

func TestMissingFromDest_SkipSameSize(t *testing.T) {
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	src := []scan.FileInfo{fi("DCIM/DSC_0001.NEF", 1024, modtime)}
	dstIndex := map[string]scan.FileInfo{
		"2026/2026-03/2026-03-25/dsc_0001.nef": fi("2026/2026-03/2026-03-25/DSC_0001.NEF", 1024, modtime),
	}

	missing := scan.MissingFromDest(src, dstIndex)
	if len(missing) != 0 {
		t.Errorf("len = %d, want 0 (already on dest, same size)", len(missing))
	}
}

func TestMissingFromDest_IncludeDifferentSize(t *testing.T) {
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	src := []scan.FileInfo{fi("DCIM/DSC_0001.NEF", 2048, modtime)}
	dstIndex := map[string]scan.FileInfo{
		"2026/2026-03/2026-03-25/dsc_0001.nef": fi("2026/2026-03/2026-03-25/DSC_0001.NEF", 1024, modtime),
	}

	missing := scan.MissingFromDest(src, dstIndex)
	if len(missing) != 1 {
		t.Errorf("len = %d, want 1 (different size = collision candidate)", len(missing))
	}
}

func TestMissingFromDest_Mixed(t *testing.T) {
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	src := []scan.FileInfo{
		fi("DCIM/DSC_0001.NEF", 1024, modtime), // new
		fi("DCIM/DSC_0002.NEF", 512, modtime),  // same size on dest → skip
		fi("DCIM/DSC_0003.NEF", 2048, modtime), // different size → include
	}
	dstIndex := map[string]scan.FileInfo{
		"2026/2026-03/2026-03-25/dsc_0002.nef": fi("2026/2026-03/2026-03-25/DSC_0002.NEF", 512, modtime),
		"2026/2026-03/2026-03-25/dsc_0003.nef": fi("2026/2026-03/2026-03-25/DSC_0003.NEF", 1024, modtime),
	}

	missing := scan.MissingFromDest(src, dstIndex)
	if len(missing) != 2 {
		t.Errorf("len = %d, want 2", len(missing))
	}
}

func TestMissingFromDest_SkipWhenCollisionCopyExists(t *testing.T) {
	// A previous run resolved the DSC_0003 name collision by saving the camera
	// file as DSC_0003_1.NEF. Re-running must NOT copy it again as _2.
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	src := []scan.FileInfo{fi("DCIM/DSC_0003.NEF", 2048, modtime)}
	dstIndex := map[string]scan.FileInfo{
		"2026/2026-03/2026-03-25/dsc_0003.nef":   fi("2026/2026-03/2026-03-25/DSC_0003.NEF", 1024, modtime),
		"2026/2026-03/2026-03-25/dsc_0003_1.nef": fi("2026/2026-03/2026-03-25/DSC_0003_1.NEF", 2048, modtime),
	}

	missing := scan.MissingFromDest(src, dstIndex)
	if len(missing) != 0 {
		t.Errorf("len = %d, want 0 (collision copy _1 already has same size)", len(missing))
	}
}

func TestMissingFromDest_IncludeWhenCollisionCopyDiffers(t *testing.T) {
	// _1 exists but with yet another size — the source file is still missing.
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	src := []scan.FileInfo{fi("DCIM/DSC_0003.NEF", 4096, modtime)}
	dstIndex := map[string]scan.FileInfo{
		"2026/2026-03/2026-03-25/dsc_0003.nef":   fi("2026/2026-03/2026-03-25/DSC_0003.NEF", 1024, modtime),
		"2026/2026-03/2026-03-25/dsc_0003_1.nef": fi("2026/2026-03/2026-03-25/DSC_0003_1.NEF", 2048, modtime),
	}

	missing := scan.MissingFromDest(src, dstIndex)
	if len(missing) != 1 {
		t.Errorf("len = %d, want 1 (no collision copy matches the size)", len(missing))
	}
}

// ── MissingByRelPath ──────────────────────────────────────────────────────────

func TestMissingByRelPath_NewFile(t *testing.T) {
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	src := []scan.FileInfo{fi("photos/2026-03-25/DSC_0001.NEF", 1024, modtime)}

	missing := scan.MissingByRelPath(src, map[string]scan.FileInfo{})
	if len(missing) != 1 {
		t.Errorf("len = %d, want 1", len(missing))
	}
}

func TestMissingByRelPath_SkipSameSize(t *testing.T) {
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	src := []scan.FileInfo{fi("photos/2026-03-25/DSC_0001.NEF", 1024, modtime)}
	dstIndex := map[string]scan.FileInfo{
		"photos/2026-03-25/dsc_0001.nef": fi("photos/2026-03-25/DSC_0001.NEF", 1024, modtime),
	}

	missing := scan.MissingByRelPath(src, dstIndex)
	if len(missing) != 0 {
		t.Errorf("len = %d, want 0", len(missing))
	}
}

func TestMissingByRelPath_IncludeDifferentSize(t *testing.T) {
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	src := []scan.FileInfo{fi("photos/2026-03-25/DSC_0001.NEF", 2048, modtime)}
	dstIndex := map[string]scan.FileInfo{
		"photos/2026-03-25/dsc_0001.nef": fi("photos/2026-03-25/DSC_0001.NEF", 1024, modtime),
	}

	missing := scan.MissingByRelPath(src, dstIndex)
	if len(missing) != 1 {
		t.Errorf("len = %d, want 1", len(missing))
	}
}

// ── SplitByCategory / WalkDual ────────────────────────────────────────────────

func TestSplitByCategory(t *testing.T) {
	modtime := time.Now()
	files := []scan.FileInfo{
		fi("a.NEF", 1, modtime),
		fi("b.MOV", 1, modtime),
		fi("c.JPG", 1, modtime),
	}
	byExt := func(f scan.FileInfo) string {
		if filepath.Ext(f.RelPath) == ".MOV" {
			return "videos"
		}
		return "photos"
	}
	photos, videos := scan.SplitByCategory(files, byExt)
	if len(photos) != 2 || len(videos) != 1 {
		t.Errorf("photos=%d videos=%d, want 2/1", len(photos), len(videos))
	}
}

func TestWalkDual_SeparateRoots(t *testing.T) {
	pRoot, vRoot := t.TempDir(), t.TempDir()
	touch(t, filepath.Join(pRoot, "a.NEF"))
	touch(t, filepath.Join(vRoot, "b.MOV"))

	photos, videos := scan.WalkDual(pRoot, vRoot, []string{".nef", ".mov"})
	if len(photos) != 1 || len(videos) != 1 {
		t.Errorf("photos=%d videos=%d, want 1/1", len(photos), len(videos))
	}
}

func TestWalkDual_MergedRoot(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "a.NEF"))
	touch(t, filepath.Join(root, "b.MOV"))

	photos, videos := scan.WalkDual(root, root, []string{".nef", ".mov"})
	// Same root → scanned once, same list returned for both categories.
	if len(photos) != 2 || len(videos) != 2 {
		t.Errorf("photos=%d videos=%d, want 2/2 (shared list)", len(photos), len(videos))
	}
}

func TestWalkDual_MissingRoot(t *testing.T) {
	pRoot := t.TempDir()
	touch(t, filepath.Join(pRoot, "a.NEF"))

	photos, videos := scan.WalkDual(pRoot, filepath.Join(pRoot, "nonexistent"), []string{".nef"})
	if len(photos) != 1 || len(videos) != 0 {
		t.Errorf("photos=%d videos=%d, want 1/0", len(photos), len(videos))
	}
}

// ── FilterByExts ─────────────────────────────────────────────────────────────

func TestFilterByExts(t *testing.T) {
	modtime := time.Now()
	files := []scan.FileInfo{
		fi("a.NEF", 1, modtime),
		fi("b.JPG", 1, modtime),
		fi("c.MOV", 1, modtime),
	}

	got := scan.FilterByExts(files, []string{".nef", ".jpg"})
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestFilterByExts_EmptyFilter(t *testing.T) {
	modtime := time.Now()
	files := []scan.FileInfo{fi("a.NEF", 1, modtime), fi("b.JPG", 1, modtime)}

	got := scan.FilterByExts(files, nil)
	if len(got) != len(files) {
		t.Errorf("len = %d, want %d (nil exts = no filtering)", len(got), len(files))
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func fi(relPath string, size int64, modtime time.Time) scan.FileInfo {
	return scan.FileInfo{RelPath: relPath, AbsPath: relPath, Size: size, ModTime: modtime}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("touch %q: %v", path, err)
	}
}
