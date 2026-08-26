package scan

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// heifOptions varies the parts of the container that differ between real
// writers, so the parser is exercised against more than one shape.
type heifOptions struct {
	infeVersion  byte // 2 (item_ID uint16) or 3 (uint32)
	ilocVersion  byte // 0, 1 or 2
	itemID       uint32
	exifLead     uint32 // bytes between the length field and the TIFF header
	construction uint64
	extraItem    bool // a picture item listed before the EXIF one
}

// buildHEIF assembles a HEIC-shaped file: ftyp, a meta box describing where
// the EXIF item lives, and an mdat holding it. Real cameras write far more
// than this, but the boxes the parser walks are the same ones.
func buildHEIF(t testing.TB, date string, opt heifOptions) []byte {
	t.Helper()
	if opt.itemID == 0 {
		opt.itemID = 1
	}

	box := func(name string, payload []byte) []byte {
		out := make([]byte, 8, 8+len(payload))
		binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
		copy(out[4:8], name)
		return append(out, payload...)
	}
	fullBox := func(name string, version byte, payload []byte) []byte {
		body := append([]byte{version, 0, 0, 0}, payload...)
		return box(name, body)
	}
	be16 := func(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
	be32 := func(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

	// The EXIF item payload: a lead-in of opt.exifLead bytes, then the TIFF
	// block the EXIF reader parses.
	tiff := buildTIFF(t, date, binary.BigEndian)
	exifItem := be32(opt.exifLead)
	exifItem = append(exifItem, bytes.Repeat([]byte{'E'}, int(opt.exifLead))...)
	exifItem = append(exifItem, tiff...)

	infe := func(id uint32, itemType string) []byte {
		var body []byte
		if opt.infeVersion == 3 {
			body = append(body, be32(id)...)
		} else {
			body = append(body, be16(uint16(id))...)
		}
		body = append(body, be16(0)...) // protection index
		body = append(body, itemType...)
		body = append(body, 0) // item_name, empty
		return fullBox("infe", opt.infeVersion, body)
	}

	var entries []byte
	count := uint16(1)
	if opt.extraItem {
		entries = append(entries, infe(opt.itemID+10, "hvc1")...)
		count++
	}
	entries = append(entries, infe(opt.itemID, "Exif")...)
	iinf := fullBox("iinf", 0, append(be16(count), entries...))

	// iloc: 4-byte offsets and lengths, no base offset, one extent per item.
	ilocBody := []byte{0x44, 0x00} // offset_size=4 length_size=4, base=0 index=0
	items := []struct {
		id     uint32
		offset uint32
		length uint32
	}{{opt.itemID, 0, uint32(len(exifItem))}}
	if opt.extraItem {
		items = append([]struct {
			id     uint32
			offset uint32
			length uint32
		}{{opt.itemID + 10, 0, 4}}, items...)
	}
	if opt.ilocVersion == 2 {
		ilocBody = append(ilocBody, be32(uint32(len(items)))...)
	} else {
		ilocBody = append(ilocBody, be16(uint16(len(items)))...)
	}
	// The item offsets are patched once the header length is known.
	type patch struct{ at int }
	var patches []patch
	for _, it := range items {
		if opt.ilocVersion == 2 {
			ilocBody = append(ilocBody, be32(it.id)...)
		} else {
			ilocBody = append(ilocBody, be16(uint16(it.id))...)
		}
		if opt.ilocVersion >= 1 {
			ilocBody = append(ilocBody, be16(uint16(opt.construction))...)
		}
		ilocBody = append(ilocBody, be16(0)...) // data_reference_index
		ilocBody = append(ilocBody, be16(1)...) // extent_count
		patches = append(patches, patch{at: len(ilocBody)})
		ilocBody = append(ilocBody, be32(it.offset)...)
		ilocBody = append(ilocBody, be32(it.length)...)
	}
	iloc := fullBox("iloc", opt.ilocVersion, ilocBody)

	ftyp := box("ftyp", []byte("heic\x00\x00\x00\x00mif1heic"))

	// mdat holds the items; the EXIF one goes last so its offset is not zero.
	// The extents are patched with real file offsets before meta is assembled,
	// since assembling it copies these bytes.
	mdatPayload := append(bytes.Repeat([]byte{0x33}, 4), exifItem...)
	const mdatHeader = 8
	metaLen := 8 + 4 + len(iinf) + len(iloc)
	exifFileOffset := len(ftyp) + metaLen + mdatHeader + 4

	body := iloc[8+4:] // past the box header and the version/flags
	for i, p := range patches {
		off := uint32(exifFileOffset)
		if opt.extraItem && i == 0 {
			off = uint32(len(ftyp) + metaLen + mdatHeader)
		}
		binary.BigEndian.PutUint32(body[p.at:p.at+4], off)
	}

	meta := fullBox("meta", 0, append(iinf, iloc...))
	if len(meta) != metaLen {
		t.Fatalf("meta box is %d bytes, offsets were computed for %d", len(meta), metaLen)
	}

	out := append([]byte{}, ftyp...)
	out = append(out, meta...)
	out = append(out, box("mdat", mdatPayload)...)
	return out
}

func writeTemp(t testing.TB, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCaptureTimeHEIF(t *testing.T) {
	want := time.Date(2026, 3, 25, 14, 30, 15, 0, time.Local)

	cases := map[string]heifOptions{
		"infe v2, iloc v0":           {infeVersion: 2, ilocVersion: 0},
		"infe v3, iloc v1":           {infeVersion: 3, ilocVersion: 1},
		"infe v2, iloc v2":           {infeVersion: 2, ilocVersion: 2},
		"Exif\\0\\0 marker in front": {infeVersion: 2, ilocVersion: 0, exifLead: 6},
		"picture item listed first":  {infeVersion: 2, ilocVersion: 1, extraItem: true, itemID: 3},
		"high item id":               {infeVersion: 3, ilocVersion: 2, itemID: 9000},
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeTemp(t, "IMG_0001.HEIC", buildHEIF(t, "2026:03:25 14:30:15", opt))

			got, ok := CaptureTime(path)
			if !ok {
				t.Fatal("no capture time read from the HEIF file")
			}
			if !got.Equal(want) {
				t.Errorf("CaptureTime = %s, want %s", got, want)
			}
		})
	}
}

// The bytes of an item can live inside the meta box instead of at a file
// offset. No camera writes EXIF that way, and reading the offset as if it were
// one would land somewhere arbitrary — so the file falls back to its modtime.
func TestCaptureTimeHEIF_IdatConstructionIsRefused(t *testing.T) {
	path := writeTemp(t, "IMG_0002.HEIC", buildHEIF(t, "2026:03:25 14:30:15",
		heifOptions{infeVersion: 2, ilocVersion: 1, construction: 1}))

	if got, ok := CaptureTime(path); ok {
		t.Errorf("CaptureTime = %s, want no time for an idat-stored item", got)
	}
}

// A still with no EXIF item at all — an AVIF written by a converter, say.
func TestCaptureTimeHEIF_NoExifItem(t *testing.T) {
	data := buildHEIF(t, "2026:03:25 14:30:15", heifOptions{infeVersion: 2, ilocVersion: 0})
	data = bytes.Replace(data, []byte("Exif"), []byte("hvc1"), 1)

	if got, ok := CaptureTime(writeTemp(t, "IMG_0003.HEIC", data)); ok {
		t.Errorf("CaptureTime = %s, want no time when nothing holds EXIF", got)
	}
}

// Truncation must not panic or hang, whatever it cuts through.
func TestCaptureTimeHEIF_Truncated(t *testing.T) {
	full := buildHEIF(t, "2026:03:25 14:30:15", heifOptions{infeVersion: 2, ilocVersion: 1})
	for n := 12; n < len(full); n += 7 {
		path := writeTemp(t, "IMG_0004.HEIC", full[:n])
		if _, ok := CaptureTime(path); ok && n < len(full)-4 {
			// A prefix may legitimately still parse once meta and mdat are
			// complete; what matters is that it returns rather than crashing.
			continue
		}
	}
}

// A video is still read from its movie header — the HEIF path must not have
// displaced it.
func TestCaptureTimeMOVStillWorks(t *testing.T) {
	want := time.Date(2026, 5, 1, 9, 0, 0, 0, time.Local)
	path := writeTemp(t, "VID_0001.MOV", buildMOV(want))

	got, ok := CaptureTime(path)
	if !ok {
		t.Fatal("no capture time read from the MOV file")
	}
	if !got.Equal(want) {
		t.Errorf("CaptureTime = %s, want %s", got, want)
	}
}
