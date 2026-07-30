//go:build ignore

// make_testdata creates synthetic camera/SSD/NAS files covering all copy scenarios.
// Run from the repo root:
//
//	go run testdata/make_testdata.go
//
// To reset and recreate:
//
//	rm -rf testdata/camera testdata/ssd testdata/nas && go run testdata/make_testdata.go
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const KB = 1024
const MB = 1024 * KB

func writeFile(path string, data []byte, modtime time.Time) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		panic(err)
	}
	os.Chtimes(path, modtime, modtime)
}

func rnd(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func main() {
	// Fixed modtime so the destination folder (photos/YYYY-MM-DD/) is predictable.
	modtime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.Local)
	date := modtime.Format("2006-01-02")

	// Capture date of the two files that carry metadata — deliberately years
	// before modtime so it is obvious which of the two the copy used.
	shotAt := time.Date(2019, 5, 4, 13, 45, 2, 0, time.Local)
	shotDate := shotAt.Format("2006-01-02")

	year := modtime.Format("2006")
	month := modtime.Format("2006-01")

	cam := "testdata/camera/DCIM/100NIKON"
	ssdPhotos := filepath.Join("testdata/ssd/photos", year, month, date)
	ssdVideos := filepath.Join("testdata/ssd/videos", year, month, date)
	nasPhotos := filepath.Join("testdata/nas/photos", year, month, date)

	// ── Shared byte slices (same content on camera and SSD/NAS for skip+verify) ──
	nef0002shared := rnd(512 * KB) // camera DSC_0002 == SSD DSC_0002

	// ── Camera files ─────────────────────────────────────────────────────────────
	//
	// DSC_0001.NEF  – 1 MB  – not on SSD                    → COPY
	// DSC_0002.NEF  – 512 KB – already on SSD, same content  → SKIP
	// DSC_0003.NEF  – 1 MB  – SSD has same name but 512 KB   → COLLISION → _1 suffix
	// DSC_0004.JPG  – 256 KB – not on SSD                    → COPY
	// VID_0001.MOV  – 2 MB  – not on SSD                     → COPY (videos/)

	// DSC_0005.JPG – 128 KB – carries EXIF from 2019, modtime is 2026 → files
	//                                                          under 2019, not 2026
	// VID_0002.MP4 – 512 KB – carries an mvhd creation time from 2019, same idea

	writeFile(filepath.Join(cam, "DSC_0001.NEF"), rnd(MB), modtime)
	writeFile(filepath.Join(cam, "DSC_0002.NEF"), nef0002shared, modtime)
	writeFile(filepath.Join(cam, "DSC_0003.NEF"), rnd(MB), modtime)
	writeFile(filepath.Join(cam, "DSC_0004.JPG"), rnd(256*KB), modtime)
	writeFile(filepath.Join(cam, "VID_0001.MOV"), rnd(2*MB), modtime)

	// Files whose metadata date differs from their modtime, so a run shows the
	// capture date winning — the case a card copied onto an external drive with
	// a file manager produces, where every modtime says "now".
	writeFile(filepath.Join(cam, "DSC_0005.JPG"), jpegWithEXIF(shotAt, 128*KB), modtime)
	writeFile(filepath.Join(cam, "VID_0002.MP4"), mp4WithCreationTime(shotAt, 512*KB), modtime)

	// ── Pre-populated SSD ────────────────────────────────────────────────────────
	//
	// DSC_0002.NEF (512 KB, identical to camera) → skip in phase 1
	// DSC_0003.NEF (512 KB, different from camera's 1 MB) → triggers collision
	writeFile(filepath.Join(ssdPhotos, "DSC_0002.NEF"), nef0002shared, modtime)
	writeFile(filepath.Join(ssdPhotos, "DSC_0003.NEF"), rnd(512*KB), modtime)

	// ── Pre-populated NAS ────────────────────────────────────────────────────────
	//
	// DSC_0002.NEF (same as SSD) → skip in phase 2
	writeFile(filepath.Join(nasPhotos, "DSC_0002.NEF"), nef0002shared, modtime)

	// ── Summary ──────────────────────────────────────────────────────────────────
	fmt.Printf(`
Testdata created  (date folder: %s)

Phase 1 – Camera → SSD
  DSC_0001.NEF   COPY      not on SSD
  DSC_0002.NEF   SKIP      already on SSD, same size+content
  DSC_0003.NEF   COPY→_1   SSD has same name but different size (512 KB vs 1 MB)
                            saved as <ssd_photos>/%s/DSC_0003_1.NEF  (year/month/day hierarchy)
  DSC_0004.JPG   COPY      not on SSD
  VID_0001.MOV   COPY      not on SSD  →  <ssd_videos>/%s/
  DSC_0005.JPG   COPY      filed under %s from EXIF, NOT %s from the modtime
  VID_0002.MP4   COPY      filed under %s from the movie header, likewise

Phase 2 – SSD → NAS
  DSC_0001.NEF     COPY    not on NAS
  DSC_0002.NEF     SKIP    already on NAS, same size+content
  DSC_0003.NEF     COPY    not on NAS  (pre-existing, 512 KB version)
  DSC_0003_1.NEF   COPY    not on NAS  (new collision copy, 1 MB)
  DSC_0004.JPG     COPY    not on NAS
  VID_0001.MOV     COPY    not on NAS

verify – expected result
  DSC_0001.NEF   OK
  DSC_0002.NEF   OK
  DSC_0003.NEF   ⚠️  SSD hash mismatch  ← expected: SSD still has the OLD 512 KB file
                                           the new copy lives as DSC_0003_1.NEF
  DSC_0004.JPG   OK
  VID_0001.MOV   OK

Run:
  go run ./cmd/camera-backup --config testdata/config.toml status
  go run ./cmd/camera-backup --config testdata/config.toml copy
  go run ./cmd/camera-backup --config testdata/config.toml verify -v

Reset:
  rm -rf testdata/camera testdata/ssd testdata/nas && go run testdata/make_testdata.go
`, date, date, date, shotDate, date, shotDate)

	_ = ssdVideos // created implicitly when VID_0001.MOV is copied
}

// jpegWithEXIF builds a size-byte file that is a valid JPEG container carrying
// shotAt as DateTimeOriginal, padded with random bytes so each run produces
// distinct content. It is not a decodable image — nothing in the copy path
// decodes pixels, and the TUI shows "[no preview]" for it.
func jpegWithEXIF(shotAt time.Time, size int) []byte {
	// TIFF block: header, IFD0 with an Exif-IFD pointer, Exif IFD with
	// DateTimeOriginal (tag 0x9003) as a 20-byte ASCII value.
	const (
		ifd0Off = 8
		exifOff = ifd0Off + 2 + 12 + 4
		dateOff = exifOff + 2 + 12 + 4
	)
	tiff := new(bytes.Buffer)
	tiff.WriteString("II*\x00")
	put32 := func(v uint32) { binary.Write(tiff, binary.LittleEndian, v) }
	put16 := func(v uint16) { binary.Write(tiff, binary.LittleEndian, v) }

	put32(ifd0Off)
	put16(1)       // IFD0: one entry
	put16(0x8769)  // ExifIFDPointer
	put16(4)       // LONG
	put32(1)       // count
	put32(exifOff) //
	put32(0)       // no next IFD
	put16(1)       // Exif IFD: one entry
	put16(0x9003)  // DateTimeOriginal
	put16(2)       // ASCII
	put32(20)      // count
	put32(dateOff) //
	put32(0)       // no next IFD
	date := make([]byte, 20)
	copy(date, shotAt.Format("2006:01:02 15:04:05"))
	tiff.Write(date)

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)

	out := new(bytes.Buffer)
	out.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1}) // SOI + APP1
	binary.Write(out, binary.BigEndian, uint16(len(payload)+2))
	out.Write(payload)
	return pad(out.Bytes(), size)
}

// mp4WithCreationTime builds a size-byte ISO-BMFF file whose moov/mvhd carries
// shotAt, the way a camera records an MP4 or MOV.
func mp4WithCreationTime(shotAt time.Time, size int) []byte {
	atom := func(name string, payload []byte) []byte {
		out := make([]byte, 8, 8+len(payload))
		binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
		copy(out[4:8], name)
		return append(out, payload...)
	}
	epoch := time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	secs := uint32(shotAt.UTC().Sub(epoch) / time.Second)

	mvhd := make([]byte, 100) // version 0; creation and modification time
	binary.BigEndian.PutUint32(mvhd[4:8], secs)
	binary.BigEndian.PutUint32(mvhd[8:12], secs)

	var out []byte
	out = append(out, atom("ftyp", []byte("isom\x00\x00\x02\x00"))...)
	out = append(out, atom("moov", atom("mvhd", mvhd))...)
	return pad(out, size)
}

// pad grows b to exactly size bytes with random data, so files differ between
// runs and hash comparisons stay meaningful.
func pad(b []byte, size int) []byte {
	if len(b) >= size {
		return b
	}
	return append(b, rnd(size-len(b))...)
}
