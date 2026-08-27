// White-box tests for the walk's error reporting — same package so the
// WalkDir callback can be driven directly.
//
// That matters more here than convenience. The failure this guards against is
// a directory the filesystem refuses to list, and the only portable way to
// arrange one is a permission bit, which root ignores: a test built solely on
// chmod passes vacuously in exactly the environments (containers, CI) where it
// runs most often. So the callback is exercised with synthesised errors, which
// works as any user, and the chmod test is kept alongside as the end-to-end
// check for the ones that can run it.
package scan

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubDirEntry is a directory entry whose Info can be made to fail — the
// "listed but unstattable" case, e.g. a file that vanishes mid-walk.
type stubDirEntry struct {
	name    string
	dir     bool
	infoErr error
}

func (s stubDirEntry) Name() string      { return s.name }
func (s stubDirEntry) IsDir() bool       { return s.dir }
func (s stubDirEntry) Type() fs.FileMode { return 0 }
func (s stubDirEntry) Info() (fs.FileInfo, error) {
	if s.infoErr != nil {
		return nil, s.infoErr
	}
	return nil, errors.New("unexpected Info call")
}

func newWalker(root string, exts ...string) *walker {
	w := &walker{root: root, extSet: map[string]struct{}{}}
	for _, e := range exts {
		w.extSet[e] = struct{}{}
	}
	return w
}

// The whole point of the type: an error handed to the callback is kept, not
// dropped, and the walk carries on so the rest of the device is still scanned.
func TestWalker_RecordsErrorAndContinues(t *testing.T) {
	w := newWalker("/card", ".nef")
	denied := fs.ErrPermission

	if err := w.visit("/card/DCIM/101NIKON", stubDirEntry{name: "101NIKON", dir: true}, denied); err != nil {
		t.Fatalf("visit returned %v, want nil so the walk continues", err)
	}
	if len(w.unreadable) != 1 {
		t.Fatalf("unreadable = %v, want the denied directory recorded", w.unreadable)
	}
	if w.unreadable[0].Path != "/card/DCIM/101NIKON" {
		t.Errorf("Path = %q, want the directory that failed", w.unreadable[0].Path)
	}
	if !errors.Is(w.unreadable[0].Err, fs.ErrPermission) {
		t.Errorf("Err = %v, want the filesystem's own error preserved", w.unreadable[0].Err)
	}
	if len(w.files) != 0 {
		t.Errorf("files = %v, want none — nothing was read", w.files)
	}
}

// A file that is listed but whose metadata cannot be read is real and this scan
// cannot describe it, so it counts as unreadable rather than as absent.
func TestWalker_RecordsInfoFailure(t *testing.T) {
	w := newWalker("/card", ".nef")
	entry := stubDirEntry{name: "DSC_0001.NEF", infoErr: errors.New("stale file handle")}

	if err := w.visit("/card/DSC_0001.NEF", entry, nil); err != nil {
		t.Fatalf("visit returned %v, want nil", err)
	}
	if len(w.unreadable) != 1 || len(w.files) != 0 {
		t.Fatalf("unreadable = %v, files = %v; want the file recorded as unreadable", w.unreadable, w.files)
	}
	if w.unreadable[0].Path != "/card/DSC_0001.NEF" {
		t.Errorf("Path = %q, want the file that could not be stat'ed", w.unreadable[0].Path)
	}
}

// A file whose extension is not configured is invisible everywhere, and an
// error reading its metadata is equally irrelevant — it was never in scope.
func TestWalker_IgnoresUnconfiguredExtensions(t *testing.T) {
	w := newWalker("/card", ".nef")
	entry := stubDirEntry{name: "NOTES.TXT", infoErr: errors.New("stale file handle")}

	if err := w.visit("/card/NOTES.TXT", entry, nil); err != nil {
		t.Fatalf("visit returned %v, want nil", err)
	}
	if len(w.unreadable) != 0 {
		t.Errorf("unreadable = %v, want none — .txt is not media", w.unreadable)
	}
}

// Several failures in one walk are all reported: a card going bad rarely loses
// exactly one directory, and a count of "1" would understate the damage.
func TestWalker_RecordsEveryFailure(t *testing.T) {
	w := newWalker("/card", ".nef")
	for _, p := range []string{"/card/DCIM/101NIKON", "/card/DCIM/102NIKON", "/card/MISC"} {
		if err := w.visit(p, nil, fs.ErrPermission); err != nil {
			t.Fatalf("visit(%s) returned %v", p, err)
		}
	}
	if len(w.unreadable) != 3 {
		t.Fatalf("unreadable = %v, want all three recorded", w.unreadable)
	}
}

func TestUnreadable_StringNamesPathAndCause(t *testing.T) {
	got := Unreadable{Path: "/card/DCIM", Err: fs.ErrPermission}.String()
	want := "/card/DCIM: permission denied"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestPaths_ReturnsPathsInOrder(t *testing.T) {
	in := []Unreadable{{Path: "/b", Err: fs.ErrPermission}, {Path: "/a", Err: fs.ErrPermission}}
	got := Paths(in)
	if len(got) != 2 || got[0] != "/b" || got[1] != "/a" {
		t.Errorf("Paths = %v, want [/b /a]", got)
	}
	if len(Paths(nil)) != 0 {
		t.Errorf("Paths(nil) = %v, want empty", Paths(nil))
	}
}

// A root that is not there at all is reported rather than passed off as an
// empty device — "the card has no photos on it" and "the card is not readable"
// must never look the same.
func TestWalk_MissingRootIsReported(t *testing.T) {
	files, unreadable, err := Walk(filepath.Join(t.TempDir(), "no-such-card"), []string{".nef"})
	if err != nil {
		t.Fatalf("Walk returned %v, want the failure reported as unreadable instead", err)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none", files)
	}
	if len(unreadable) != 1 {
		t.Fatalf("unreadable = %v, want the missing root reported", unreadable)
	}
}

// The end-to-end check: a real directory the process may not list. Root
// ignores the permission bits, so there it is the white-box tests above that
// carry the guarantee.
func TestWalk_UnreadableDirectoryIsReportedAndTheRestIsStillScanned(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply, see the walker tests above")
	}
	card := t.TempDir()
	readable := filepath.Join(card, "DCIM", "100NIKON")
	locked := filepath.Join(card, "DCIM", "101NIKON")
	for _, d := range []string{readable, locked} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{
		filepath.Join(readable, "DSC_0001.NEF"),
		filepath.Join(locked, "DSC_0002.NEF"),
	} {
		if err := os.WriteFile(p, []byte("raw"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	files, unreadable, err := Walk(card, []string{".nef"})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(files) != 1 || files[0].RelPath != "DCIM/100NIKON/DSC_0001.NEF" {
		t.Errorf("files = %v, want only the readable one — the walk must not stop", files)
	}
	if len(unreadable) != 1 {
		t.Fatalf("unreadable = %v, want the locked directory reported", unreadable)
	}
	if unreadable[0].Path != locked {
		t.Errorf("unreadable path = %q, want %q", unreadable[0].Path, locked)
	}
}

// WalkSource is what every source scan actually calls, so it has to pass the
// list on rather than reduce the device to the files it happened to reach.
func TestWalkSource_PropagatesUnreadablePaths(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	card := t.TempDir()
	locked := filepath.Join(card, "DCIM")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(locked, "DSC_0001.NEF")
	if err := os.WriteFile(p, []byte("raw"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	files, unreadable, err := WalkSource(card, []string{".nef"})
	if err != nil {
		t.Fatalf("WalkSource: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none — the only directory was unreadable", files)
	}
	if len(unreadable) == 0 {
		t.Fatal("WalkSource reported nothing unreadable; a source scan that hides this certifies a backup nobody made")
	}
}
