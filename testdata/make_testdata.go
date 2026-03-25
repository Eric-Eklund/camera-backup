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
	"crypto/rand"
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

	cam := "testdata/camera/DCIM/100NIKON"
	ssdPhotos := filepath.Join("testdata/ssd/photos", date)
	ssdVideos := filepath.Join("testdata/ssd/videos", date)
	nasPhotos := filepath.Join("testdata/nas/photos", date)

	// ── Shared byte slices (same content on camera and SSD/NAS for skip+verify) ──
	nef0002shared := rnd(512 * KB) // camera DSC_0002 == SSD DSC_0002

	// ── Camera files ─────────────────────────────────────────────────────────────
	//
	// DSC_0001.NEF  – 1 MB  – not on SSD                    → COPY
	// DSC_0002.NEF  – 512 KB – already on SSD, same content  → SKIP
	// DSC_0003.NEF  – 1 MB  – SSD has same name but 512 KB   → COLLISION → _1 suffix
	// DSC_0004.JPG  – 256 KB – not on SSD                    → COPY
	// VID_0001.MOV  – 2 MB  – not on SSD                     → COPY (videos/)

	writeFile(filepath.Join(cam, "DSC_0001.NEF"), rnd(MB), modtime)
	writeFile(filepath.Join(cam, "DSC_0002.NEF"), nef0002shared, modtime)
	writeFile(filepath.Join(cam, "DSC_0003.NEF"), rnd(MB), modtime)
	writeFile(filepath.Join(cam, "DSC_0004.JPG"), rnd(256*KB), modtime)
	writeFile(filepath.Join(cam, "VID_0001.MOV"), rnd(2*MB), modtime)

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
                            saved as photos/%s/DSC_0003_1.NEF
  DSC_0004.JPG   COPY      not on SSD
  VID_0001.MOV   COPY      not on SSD  →  videos/%s/

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
`, date, date, date)

	_ = ssdVideos // created implicitly when VID_0001.MOV is copied
}
