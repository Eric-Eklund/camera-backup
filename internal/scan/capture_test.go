package scan

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseEXIFDate(t *testing.T) {
	tests := []struct {
		in   string
		want string // empty = not a usable date
	}{
		{"2010:07:20 17:27:12", "2010-07-20 17:27:12"},
		{"2010:07:20 17:27:12\x00", "2010-07-20 17:27:12"},
		{"  2016:09:22 17:10:29  ", "2016-09-22 17:10:29"},
		{"0000:00:00 00:00:00", ""}, // camera clock never set
		{"                   ", ""},
		{"2010:07:20", ""}, // too short
		{"", ""},
	}
	for _, tc := range tests {
		got, ok := parseEXIFDate(tc.in)
		if tc.want == "" {
			if ok {
				t.Errorf("parseEXIFDate(%q) = %v, want not ok", tc.in, got)
			}
			continue
		}
		if !ok {
			t.Errorf("parseEXIFDate(%q) not ok, want %s", tc.in, tc.want)
			continue
		}
		if s := got.Format("2006-01-02 15:04:05"); s != tc.want {
			t.Errorf("parseEXIFDate(%q) = %s, want %s", tc.in, s, tc.want)
		}
	}
}

// TestCaptureTimeJPEG builds a minimal JPEG with an APP1/Exif block and checks
// DateTimeOriginal is found, including when a non-Exif APP segment precedes it.
func TestCaptureTimeJPEG(t *testing.T) {
	dir := t.TempDir()
	want := time.Date(2019, 5, 4, 13, 45, 2, 0, time.Local)

	for _, tc := range []struct {
		name    string
		leading bool // insert an APP0/JFIF segment before the Exif one
	}{
		{"exif_first", false},
		{"after_jfif", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".jpg")
			if err := os.WriteFile(path, buildJPEG(t, "2019:05:04 13:45:02", tc.leading), 0o600); err != nil {
				t.Fatal(err)
			}
			got, ok := CaptureTime(path)
			if !ok {
				t.Fatal("CaptureTime not ok")
			}
			if !got.Equal(want) {
				t.Errorf("CaptureTime = %v, want %v", got, want)
			}
		})
	}
}

// TestCaptureTimeTIFF covers the RAW path: NEF, CR2, ARW and friends are TIFF
// files, so the same parser reads them straight from offset 0.
func TestCaptureTimeTIFF(t *testing.T) {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		path := filepath.Join(t.TempDir(), "shot.nef")
		if err := os.WriteFile(path, buildTIFF(t, "2010:07:20 17:27:12", bo), 0o600); err != nil {
			t.Fatal(err)
		}
		got, ok := CaptureTime(path)
		if !ok {
			t.Fatalf("%v: CaptureTime not ok", bo)
		}
		want := time.Date(2010, 7, 20, 17, 27, 12, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("%v: CaptureTime = %v, want %v", bo, got, want)
		}
	}
}

// TestCaptureTimeRAF covers Fujifilm's container, which is not TIFF: the header
// points at a wrapped JPEG whose EXIF carries the date.
func TestCaptureTimeRAF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "DSCF0001.RAF")
	if err := os.WriteFile(path, buildRAF(t, "2024:10:12 21:01:01"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := CaptureTime(path)
	if !ok {
		t.Fatal("CaptureTime not ok")
	}
	want := time.Date(2024, 10, 12, 21, 1, 1, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("CaptureTime = %v, want %v", got, want)
	}
}

// TestCaptureTimeRAFBadOffset keeps a corrupt header from being read as a date.
func TestCaptureTimeRAFBadOffset(t *testing.T) {
	raf := buildRAF(t, "2024:10:12 21:01:01")
	binary.BigEndian.PutUint32(raf[rafJPEGOffset:], 0) // no JPEG recorded
	path := filepath.Join(t.TempDir(), "broken.RAF")
	if err := os.WriteFile(path, raf, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := CaptureTime(path); ok {
		t.Errorf("CaptureTime = %v, want not ok", got)
	}
}

// TestCaptureTimeMOV covers the video path — a Nikon MOV carries its recording
// time in the moov/mvhd atom, not in EXIF.
func TestCaptureTimeMOV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mov")
	want := time.Date(2021, 3, 14, 9, 26, 53, 0, time.UTC)
	if err := os.WriteFile(path, buildMOV(want), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := CaptureTime(path)
	if !ok {
		t.Fatal("CaptureTime not ok")
	}
	if !got.Equal(want) {
		t.Errorf("CaptureTime = %v, want %v", got, want)
	}
}

// TestCaptureTimeNoMetadata is the fallback case: files with nothing to read
// must report "unknown" so the caller keeps using the modtime.
func TestCaptureTimeNoMetadata(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"empty.jpg":  {},
		"plain.jpg":  {0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02}, // straight to SOS
		"random.dat": bytes.Repeat([]byte{0x7F}, 64),
		"trunc.nef":  {'I', 'I', 42, 0, 0xFF, 0xFF, 0xFF, 0x7F}, // IFD offset past EOF
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok := CaptureTime(path); ok {
			t.Errorf("CaptureTime(%s) = %v, want not ok", name, got)
		}
	}
	if got, ok := CaptureTime(filepath.Join(dir, "does-not-exist.jpg")); ok {
		t.Errorf("CaptureTime on missing file = %v, want not ok", got)
	}
}

// TestDateTakenFallback pins the precedence DestRelPath depends on.
func TestDateTakenFallback(t *testing.T) {
	mod := time.Date(2026, 7, 30, 11, 0, 0, 0, time.Local)
	shot := time.Date(2010, 7, 20, 17, 27, 12, 0, time.Local)

	withEXIF := FileInfo{RelPath: "DCIM/DSC_0001.NEF", ModTime: mod, CaptureTime: shot}
	if got := withEXIF.DateTaken(); !got.Equal(shot) {
		t.Errorf("DateTaken = %v, want capture time %v", got, shot)
	}
	if got, want := withEXIF.DestRelPath(), "2010/2010-07/2010-07-20/DSC_0001.NEF"; got != want {
		t.Errorf("DestRelPath = %q, want %q", got, want)
	}

	noEXIF := FileInfo{RelPath: "DCIM/DSC_0002.NEF", ModTime: mod}
	if got := noEXIF.DateTaken(); !got.Equal(mod) {
		t.Errorf("DateTaken = %v, want modtime %v", got, mod)
	}
	if got, want := noEXIF.DestRelPath(), "2026/2026-07/2026-07-30/DSC_0002.NEF"; got != want {
		t.Errorf("DestRelPath = %q, want %q", got, want)
	}
}

// TestSplitStableUsesModTime guards the one place that must *not* follow the
// capture time: a file written to the card seconds ago is still being written,
// however old the shot in it is.
func TestSplitStableUsesModTime(t *testing.T) {
	now := time.Date(2026, 7, 30, 11, 0, 0, 0, time.Local)
	files := []FileInfo{{
		RelPath:     "DCIM/DSC_0001.NEF",
		ModTime:     now.Add(-2 * time.Second),
		CaptureTime: time.Date(2010, 7, 20, 17, 27, 12, 0, time.Local),
	}}
	stable, unstable := SplitStable(files, now, StableAge)
	if len(stable) != 0 || len(unstable) != 1 {
		t.Errorf("SplitStable = %d stable / %d unstable, want 0/1", len(stable), len(unstable))
	}
}

// TestFillCaptureTimes checks the concurrent filler writes each result back to
// the right element and leaves metadata-less files alone.
func TestFillCaptureTimes(t *testing.T) {
	dir := t.TempDir()
	files := make([]FileInfo, 0, 20)
	for i := 0; i < 20; i++ {
		name := filepath.Join(dir, string(rune('a'+i))+".jpg")
		date := time.Date(2000+i, 1, 2, 3, 4, 5, 0, time.Local)
		if i%2 == 0 {
			if err := os.WriteFile(name, buildJPEG(t, date.Format("2006:01:02 15:04:05"), false), 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(name, []byte{0xFF, 0xD8, 0xFF, 0xD9}, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		files = append(files, FileInfo{RelPath: filepath.Base(name), AbsPath: name})
	}

	FillCaptureTimes(files)

	for i, f := range files {
		if i%2 != 0 {
			if !f.CaptureTime.IsZero() {
				t.Errorf("file %d: CaptureTime = %v, want zero", i, f.CaptureTime)
			}
			continue
		}
		want := time.Date(2000+i, 1, 2, 3, 4, 5, 0, time.Local)
		if !f.CaptureTime.Equal(want) {
			t.Errorf("file %d: CaptureTime = %v, want %v", i, f.CaptureTime, want)
		}
	}
}

// ── test file builders ────────────────────────────────────────────────────────

// buildTIFF writes a little TIFF whose IFD0 points at an Exif sub-IFD holding
// DateTimeOriginal — the same shape as the head of a NEF or CR2.
func buildTIFF(t testing.TB, date string, bo binary.ByteOrder) []byte {
	t.Helper()
	var buf bytes.Buffer
	if bo == binary.BigEndian {
		buf.WriteString("MM\x00*")
	} else {
		buf.WriteString("II*\x00")
	}
	put32 := func(v uint32) { b := make([]byte, 4); bo.PutUint32(b, v); buf.Write(b) }
	put16 := func(v uint16) { b := make([]byte, 2); bo.PutUint16(b, v); buf.Write(b) }

	const (
		ifd0Off  = 8
		exifOff  = 8 + 2 + 12 + 4       // after IFD0 (1 entry + next-IFD link)
		dateOff  = exifOff + 2 + 12 + 4 // after the Exif IFD
		dateSize = 20
	)
	put32(ifd0Off)

	// IFD0: one entry, the Exif IFD pointer.
	put16(1)
	put16(tagExifIFDPointer)
	put16(4) // LONG
	put32(1)
	put32(exifOff)
	put32(0) // no next IFD

	// Exif IFD: one entry, DateTimeOriginal as an ASCII offset.
	put16(1)
	put16(tagDateTimeOriginal)
	put16(2) // ASCII
	put32(dateSize)
	put32(dateOff)
	put32(0)

	b := buf.Bytes()
	if len(b) != dateOff {
		t.Fatalf("builder offsets drifted: wrote %d bytes, date offset %d", len(b), dateOff)
	}
	val := make([]byte, dateSize)
	copy(val, date)
	return append(b, val...)
}

// buildJPEG wraps a TIFF block in an APP1/Exif segment, optionally behind an
// APP0/JFIF segment so the marker walk has something to skip.
func buildJPEG(t testing.TB, date string, leadingAPP0 bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	segment := func(marker byte, payload []byte) {
		buf.Write([]byte{0xFF, marker})
		size := make([]byte, 2)
		binary.BigEndian.PutUint16(size, uint16(len(payload)+2))
		buf.Write(size)
		buf.Write(payload)
	}

	if leadingAPP0 {
		segment(0xE0, append([]byte("JFIF\x00"), bytes.Repeat([]byte{0}, 9)...))
	}
	segment(0xE1, append([]byte("Exif\x00\x00"), buildTIFF(t, date, binary.LittleEndian)...))
	buf.Write([]byte{0xFF, 0xD9}) // EOI
	return buf.Bytes()
}

// buildMOV writes an ISO-BMFF skeleton: ftyp, a dummy mdat (so moov is not the
// first atom, as in real camera files) and moov/mvhd carrying creation time.
func buildMOV(created time.Time) []byte {
	atom := func(name string, payload []byte) []byte {
		out := make([]byte, 8, 8+len(payload))
		binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
		copy(out[4:8], name)
		return append(out, payload...)
	}

	secs := uint32(created.Sub(quickTimeEpoch) / time.Second)
	mvhd := make([]byte, 100)
	// version 0 + flags are already zero; creation and modification time follow.
	binary.BigEndian.PutUint32(mvhd[4:8], secs)
	binary.BigEndian.PutUint32(mvhd[8:12], secs)

	var out []byte
	out = append(out, atom("ftyp", []byte("qt  \x00\x00\x02\x00"))...)
	out = append(out, atom("mdat", bytes.Repeat([]byte{0x11}, 64))...)
	out = append(out, atom("moov", atom("mvhd", mvhd))...)
	return out
}

// ── basename+size matches confirmed against the capture time ─────────────────

// destFile builds a destination FileInfo backed by a real file carrying date,
// so MissingFromDest can read its capture time.
func destFile(t testing.TB, dir, relPath, date string, size int) FileInfo {
	t.Helper()
	abs := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	data := buildJPEG(t, date, false)
	if size > len(data) {
		data = append(data, make([]byte, size-len(data))...)
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return FileInfo{RelPath: filepath.ToSlash(relPath), AbsPath: abs, Size: int64(len(data))}
}

// TestMissingFromDest_DifferentShotSameNameSize is the case three SD cards
// create: every card numbers its first frame DSC_0001, and two of those can
// share a byte-exact size. The destination copy is a *different* photograph, so
// the source must still be copied — basename+size alone would have skipped it.
func TestMissingFromDest_DifferentShotSameNameSize(t *testing.T) {
	dir := t.TempDir()
	// Already backed up: shot on 2026-05-10.
	dst := destFile(t, dir, "2026/2026-05/2026-05-10/DSC_0001.JPG", "2026:05:10 14:31:00", 4096)

	// On the card: same name, same size, shot on a different day.
	src := []FileInfo{{
		RelPath: "DCIM/DSC_0001.JPG", AbsPath: filepath.Join(dir, "card/DSC_0001.JPG"),
		Size:        dst.Size,
		ModTime:     time.Date(2026, 6, 21, 9, 16, 0, 0, time.Local),
		CaptureTime: time.Date(2026, 6, 21, 9, 16, 0, 0, time.Local),
	}}

	missing := MissingFromDest(src, IndexByRelPath([]FileInfo{dst}))
	if len(missing) != 1 {
		t.Fatalf("missing = %d files, want 1 — a different shot must not be skipped", len(missing))
	}
}

// TestMissingFromDest_SameShotUnderOldDate is the migration case: the copy is
// the same photograph, just filed under the modtime's date by an older version.
// Confirming the capture time must recognise it and skip the source.
func TestMissingFromDest_SameShotUnderOldDate(t *testing.T) {
	dir := t.TempDir()
	const shot = "2026:06:21 09:16:00"
	dst := destFile(t, dir, "2026/2026-07/2026-07-28/DSC_0001.JPG", shot, 4096)

	src := []FileInfo{{
		RelPath: "DCIM/DSC_0001.JPG", AbsPath: dst.AbsPath, Size: dst.Size,
		ModTime:     time.Date(2026, 7, 28, 20, 0, 0, 0, time.Local),
		CaptureTime: time.Date(2026, 6, 21, 9, 16, 0, 0, time.Local),
	}}

	if missing := MissingFromDest(src, IndexByRelPath([]FileInfo{dst})); len(missing) != 0 {
		t.Errorf("missing = %v, want none — same shot, only filed under another date", missing)
	}
}

// TestMissingFromDest_NoCaptureTimeTrustsNameSize keeps the old behaviour for
// files with no metadata to compare: their date path is derived from the
// modtime, so a basename+size match anywhere is the best evidence available.
func TestMissingFromDest_NoCaptureTimeTrustsNameSize(t *testing.T) {
	dir := t.TempDir()
	dst := destFile(t, dir, "2026/2026-07/2026-07-28/CLIP.MOV", "2026:06:21 09:16:00", 4096)

	src := []FileInfo{{
		RelPath: "DCIM/CLIP.MOV", AbsPath: filepath.Join(dir, "card/CLIP.MOV"),
		Size: dst.Size, ModTime: time.Date(2026, 6, 21, 9, 16, 0, 0, time.Local),
		// CaptureTime deliberately zero.
	}}

	if missing := MissingFromDest(src, IndexByRelPath([]FileInfo{dst})); len(missing) != 0 {
		t.Errorf("missing = %v, want none — no capture time, so basename+size decides", missing)
	}
}

// TestMissingFromDest_PreservesSourceOrder guards the two-pass split: the batch
// order the callers apply (videos first, size-asc) assumes the returned list
// still reflects the scan order.
func TestMissingFromDest_PreservesSourceOrder(t *testing.T) {
	dir := t.TempDir()
	// One destination copy that will need a capture-time confirmation, sitting
	// between two sources that are plainly missing.
	dst := destFile(t, dir, "2026/2026-07/2026-07-28/DSC_0002.JPG", "2026:01:01 00:00:00", 4096)

	mk := func(name string, size int64, capture time.Time) FileInfo {
		return FileInfo{
			RelPath: "DCIM/" + name, AbsPath: filepath.Join(dir, "card", name),
			Size: size, ModTime: capture, CaptureTime: capture,
		}
	}
	shot := time.Date(2026, 6, 21, 9, 0, 0, 0, time.Local)
	src := []FileInfo{
		mk("DSC_0001.JPG", 1111, shot),
		mk("DSC_0002.JPG", dst.Size, shot), // basename+size twin, different shot
		mk("DSC_0003.JPG", 3333, shot),
	}

	missing := MissingFromDest(src, IndexByRelPath([]FileInfo{dst}))
	var got []string
	for _, f := range missing {
		got = append(got, filepath.Base(f.RelPath))
	}
	want := []string{"DSC_0001.JPG", "DSC_0002.JPG", "DSC_0003.JPG"}
	if len(got) != len(want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missing = %v, want %v (source order)", got, want)
		}
	}
}

// buildRAF wraps a JPEG in a Fujifilm RAF container: the "FUJIFILM" magic, then
// the wrapped JPEG's offset and length at 0x54, big-endian.
func buildRAF(t testing.TB, date string) []byte {
	t.Helper()
	jpg := buildJPEG(t, date, false)
	const headerLen = 0x5c
	out := make([]byte, headerLen)
	copy(out, "FUJIFILMCCD-RAW 0201FF129502")
	binary.BigEndian.PutUint32(out[rafJPEGOffset:], uint32(headerLen))
	binary.BigEndian.PutUint32(out[rafJPEGOffset+4:], uint32(len(jpg)))
	return append(out, jpg...)
}
