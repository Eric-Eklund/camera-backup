package scan

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// CaptureTime reads the time a file was recorded by the camera.
//
// The filesystem modtime is unreliable for this: copying a card onto an
// external drive with a file manager, restoring a backup, or unzipping an
// archive all stamp the file with "now", which would file the shot under the
// date it was copied instead of the date it was taken.
//
// Photos are read from EXIF (DateTimeOriginal, then DateTimeDigitized, then the
// IFD0 DateTime), videos from the QuickTime/ISO-BMFF movie header. Both are
// parsed directly — no exiftool needed, so a scan works on a bare system.
// ok is false when the file carries no usable timestamp; callers fall back to
// the modtime.
func CaptureTime(absPath string) (t time.Time, ok bool) {
	f, err := os.Open(absPath)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	var head [12]byte
	n, err := io.ReadFull(f, head[:])
	if err != nil && n < 8 {
		return time.Time{}, false
	}

	switch {
	case head[0] == 0xFF && head[1] == 0xD8: // JPEG
		t, err = jpegCaptureTime(f, 0)
	case isTIFFHeader(head[:]):
		// TIFF and every TIFF-based RAW: NEF, CR2, ARW, DNG, ORF, RW2, PEF…
		t, err = tiffCaptureTime(f, 0)
	case string(head[:8]) == "FUJIFILM": // Fujifilm RAF wraps a full JPEG
		t, err = rafCaptureTime(f)
	case string(head[4:8]) == "ftyp": // ISO-BMFF: MP4, MOV, M4V
		t, err = bmffCaptureTime(f)
	default:
		// Unrecognised container (HEIC/AVIF/WebP among them): the caller falls
		// back to the modtime.
		return time.Time{}, false
	}
	if err != nil || t.IsZero() {
		return time.Time{}, false
	}
	return t, true
}

// isTIFFHeader recognises a TIFF byte-order mark. Some RAW formats replace the
// magic 42 with their own number (Olympus ORF uses 'O','R'/0x4F52, Panasonic
// RW2 uses 0x0055) but keep the layout, so only the byte order is checked and
// the offset is read accordingly.
func isTIFFHeader(head []byte) bool {
	return (head[0] == 'I' && head[1] == 'I') || (head[0] == 'M' && head[1] == 'M')
}

// EXIF/TIFF tag IDs.
const (
	tagDateTime         = 0x0132 // IFD0: file change date, the weakest signal
	tagExifIFDPointer   = 0x8769
	tagDateTimeOriginal = 0x9003 // when the shutter fired
	tagCreateDate       = 0x9004 // DateTimeDigitized
)

// jpegCaptureTime finds the APP1/Exif segment of the JPEG starting at base and
// parses the TIFF block inside it. base is non-zero for a JPEG embedded in
// another container, as in a Fujifilm RAF.
func jpegCaptureTime(r io.ReadSeeker, base int64) (time.Time, error) {
	if _, err := r.Seek(base+2, io.SeekStart); err != nil { // past SOI
		return time.Time{}, err
	}
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:2]); err != nil {
			return time.Time{}, err
		}
		if hdr[0] != 0xFF {
			return time.Time{}, errors.New("exif: not at a JPEG marker")
		}
		marker := hdr[1]
		// SOS starts entropy-coded data; no metadata beyond this point.
		if marker == 0xDA || marker == 0xD9 {
			return time.Time{}, errNoDate
		}
		// Standalone markers carry no length.
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			continue
		}
		if _, err := io.ReadFull(r, hdr[2:4]); err != nil {
			return time.Time{}, err
		}
		size := int64(binary.BigEndian.Uint16(hdr[2:4])) - 2
		if size < 0 {
			return time.Time{}, errors.New("exif: bad segment length")
		}
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return time.Time{}, err
		}
		if marker == 0xE1 && size >= 6 {
			var sig [6]byte
			if _, err := io.ReadFull(r, sig[:]); err == nil && string(sig[:4]) == "Exif" {
				return tiffCaptureTime(r, pos+6)
			}
		}
		if _, err := r.Seek(pos+size, io.SeekStart); err != nil {
			return time.Time{}, err
		}
	}
}

var errNoDate = errors.New("exif: no capture date")

// rafJPEGOffset is where a Fujifilm RAF header stores the offset of its
// embedded full-size JPEG (big-endian, followed by that JPEG's length).
const rafJPEGOffset = 0x54

// rafCaptureTime reads the EXIF of the JPEG that a Fujifilm RAF wraps — the RAF
// container itself is not TIFF, so the header has to be followed first.
func rafCaptureTime(r io.ReadSeeker) (time.Time, error) {
	var buf [4]byte
	if _, err := readAt(r, rafJPEGOffset, buf[:]); err != nil {
		return time.Time{}, err
	}
	off := int64(binary.BigEndian.Uint32(buf[:]))
	if off <= 0 {
		return time.Time{}, errNoDate
	}
	if _, err := readAt(r, off, buf[:2]); err != nil {
		return time.Time{}, err
	}
	if buf[0] != 0xFF || buf[1] != 0xD8 {
		return time.Time{}, errors.New("raf: no JPEG at the recorded offset")
	}
	return jpegCaptureTime(r, off)
}

// tiffCaptureTime parses the TIFF structure starting at base and returns the
// best capture timestamp it holds. It reads IFD0 and, if present, the Exif
// sub-IFD; it deliberately does not chase every sub-IFD a RAW file may contain.
func tiffCaptureTime(r io.ReadSeeker, base int64) (time.Time, error) {
	var hdr [8]byte
	if _, err := readAt(r, base, hdr[:]); err != nil {
		return time.Time{}, err
	}
	var bo binary.ByteOrder
	switch {
	case hdr[0] == 'I' && hdr[1] == 'I':
		bo = binary.LittleEndian
	case hdr[0] == 'M' && hdr[1] == 'M':
		bo = binary.BigEndian
	default:
		return time.Time{}, errors.New("exif: bad byte order mark")
	}

	ifd0, exifIFD, err := readIFD(r, base, int64(bo.Uint32(hdr[4:8])), bo)
	if err != nil {
		return time.Time{}, err
	}
	if exifIFD > 0 {
		exif, _, err := readIFD(r, base, exifIFD, bo)
		if err == nil {
			if t, ok := pickDate(exif); ok {
				return t, nil
			}
		}
	}
	if t, ok := pickDate(ifd0); ok {
		return t, nil
	}
	return time.Time{}, errNoDate
}

// pickDate returns the most authoritative timestamp present in one IFD.
func pickDate(tags map[uint16]string) (time.Time, bool) {
	for _, id := range []uint16{tagDateTimeOriginal, tagCreateDate, tagDateTime} {
		if t, ok := parseEXIFDate(tags[id]); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// maxIFDEntries caps how many directory entries are read from one IFD, so a
// corrupt offset cannot turn into an enormous read.
const maxIFDEntries = 512

// readIFD reads the ASCII entries of the IFD at offset (relative to base) and
// returns them by tag, plus the Exif sub-IFD offset if the directory has one.
func readIFD(r io.ReadSeeker, base, offset int64, bo binary.ByteOrder) (map[uint16]string, int64, error) {
	if offset <= 0 {
		return nil, 0, errors.New("exif: bad IFD offset")
	}
	var countBuf [2]byte
	if _, err := readAt(r, base+offset, countBuf[:]); err != nil {
		return nil, 0, err
	}
	count := int(bo.Uint16(countBuf[:]))
	if count > maxIFDEntries {
		count = maxIFDEntries
	}
	entries := make([]byte, 12*count)
	if _, err := readAt(r, base+offset+2, entries); err != nil {
		return nil, 0, err
	}

	tags := make(map[uint16]string, 4)
	var exifIFD int64
	for i := 0; i < count; i++ {
		e := entries[i*12 : i*12+12]
		tag := bo.Uint16(e[0:2])
		typ := bo.Uint16(e[2:4])
		n := int64(bo.Uint32(e[4:8]))

		if tag == tagExifIFDPointer {
			exifIFD = int64(bo.Uint32(e[8:12]))
			continue
		}
		if tag != tagDateTime && tag != tagDateTimeOriginal && tag != tagCreateDate {
			continue
		}
		// Dates are ASCII (type 2, one byte per element) and 20 bytes long, so
		// they never fit in the 4-byte value field — the entry holds an offset.
		if typ != 2 || n < 19 || n > 64 {
			continue
		}
		buf := make([]byte, n)
		if _, err := readAt(r, base+int64(bo.Uint32(e[8:12])), buf); err != nil {
			continue
		}
		tags[tag] = string(buf)
	}
	return tags, exifIFD, nil
}

func readAt(r io.ReadSeeker, off int64, buf []byte) (int, error) {
	if off < 0 {
		return 0, errors.New("exif: negative offset")
	}
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(r, buf)
}

// parseEXIFDate parses the EXIF "YYYY:MM:DD HH:MM:SS" form. EXIF timestamps
// carry no zone — they are the camera's local wall clock, which is exactly what
// the date directories should reflect, so they are interpreted as local time.
// Cameras write all-zero or blank dates when the clock was never set; those are
// rejected so the caller falls back to the modtime.
func parseEXIFDate(s string) (time.Time, bool) {
	s = strings.TrimRight(strings.TrimSpace(s), "\x00")
	if len(s) < 19 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006:01:02 15:04:05", s[:19], time.Local)
	if err != nil || t.Year() < 1900 {
		return time.Time{}, false
	}
	return t, true
}

// quickTimeEpoch is the zero point of QuickTime/ISO-BMFF timestamps.
var quickTimeEpoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)

// bmffCaptureTime reads the creation time from an MP4/MOV movie header.
// The moov atom may sit before or after the media data, so top-level atoms are
// walked by seeking rather than reading the file.
func bmffCaptureTime(r io.ReadSeeker) (time.Time, error) {
	moovOff, moovSize, err := findAtom(r, 0, 0, "moov")
	if err != nil {
		return time.Time{}, err
	}
	mvhdOff, _, err := findAtom(r, moovOff, moovSize, "mvhd")
	if err != nil {
		return time.Time{}, err
	}

	var buf [12]byte
	if _, err := readAt(r, mvhdOff, buf[:]); err != nil {
		return time.Time{}, err
	}
	version := buf[0]
	var secs uint64
	if version == 1 {
		secs = binary.BigEndian.Uint64(buf[4:12])
	} else {
		secs = uint64(binary.BigEndian.Uint32(buf[4:8]))
	}
	if secs == 0 {
		return time.Time{}, errNoDate
	}
	t := quickTimeEpoch.Add(time.Duration(secs) * time.Second)
	// Written as UTC by spec; presented in local time like the EXIF dates so
	// both kinds of file sort into the same day directory.
	return t.Local(), nil
}

// findAtom scans the atom list starting at off for name and returns the offset
// and size of its payload. limit bounds the search (0 = to end of file).
func findAtom(r io.ReadSeeker, off, limit int64, name string) (int64, int64, error) {
	end := limit
	if end == 0 {
		size, err := r.Seek(0, io.SeekEnd)
		if err != nil {
			return 0, 0, err
		}
		end = size
	} else {
		end += off
	}

	for off+8 <= end {
		var hdr [8]byte
		if _, err := readAt(r, off, hdr[:]); err != nil {
			return 0, 0, err
		}
		size := int64(binary.BigEndian.Uint32(hdr[0:4]))
		header := int64(8)
		switch size {
		case 0: // extends to end of file
			size = end - off
		case 1: // 64-bit size follows the type
			var ext [8]byte
			if _, err := readAt(r, off+8, ext[:]); err != nil {
				return 0, 0, err
			}
			size = int64(binary.BigEndian.Uint64(ext[:]))
			header = 16
		}
		if size < header {
			return 0, 0, errors.New("bmff: bad atom size")
		}
		if string(hdr[4:8]) == name {
			return off + header, size - header, nil
		}
		off += size
	}
	return 0, 0, errNoDate
}

// FillCaptureTimes reads the capture timestamp of every file in place.
//
// Only source devices need this — destination scans are compared by relative
// path, and reading headers for thousands of files over a NAS share would be
// slow for no benefit. Files are read concurrently since each one is a couple
// of small seeks and the cost is dominated by I/O latency.
func FillCaptureTimes(files []FileInfo) {
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 1 || len(files) < 2 {
		workers = 1
	}

	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				if t, ok := CaptureTime(files[i].AbsPath); ok {
					files[i].CaptureTime = t
				}
			}
		}()
	}
	for i := range files {
		idx <- i
	}
	close(idx)
	wg.Wait()
}

// captureTimesOf reads the capture time of each path, returning the ones that
// have a usable timestamp keyed by their index in paths. Duplicate paths are
// read once. Used to confirm a basename+size match against real metadata, which
// is why it is worth the bounded concurrency: these reads may be over a NAS
// share where latency, not bandwidth, is the cost.
func captureTimesOf(paths []string) map[int]time.Time {
	unique := make(map[string][]int, len(paths))
	for i, p := range paths {
		unique[p] = append(unique[p], i)
	}

	type result struct {
		path string
		t    time.Time
	}
	jobs := make(chan string)
	results := make(chan result)

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 1 || len(unique) < 2 {
		workers = 1
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if t, ok := CaptureTime(p); ok {
					results <- result{path: p, t: t}
				}
			}
		}()
	}
	go func() {
		for p := range unique {
			jobs <- p
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	out := make(map[int]time.Time, len(paths))
	for r := range results {
		for _, i := range unique[r.path] {
			out[i] = r.t
		}
	}
	return out
}

// WalkSource scans a source device like Walk and additionally reads each file's
// capture time, so DestRelPath files a shot under the date it was taken rather
// than the date the file happened to be written.
func WalkSource(root string, exts []string) ([]FileInfo, error) {
	files, err := Walk(root, exts)
	if err != nil {
		return nil, err
	}
	FillCaptureTimes(files)
	return files, nil
}
