package scan

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzCaptureTime feeds the metadata parser arbitrary bytes. The parser runs
// on every file of an untrusted card — a failing card hands back corrupt
// headers, and a scan must never panic, hang, or allocate without bound on
// one. Any parse outcome is acceptable; the property under test is that there
// is an outcome.
//
// Seeded with each container the parser recognises, so mutation starts from
// inputs that reach deep into every branch rather than falling at the first
// magic-number check.
func FuzzCaptureTime(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xFF, 0xD8})
	f.Add(buildJPEG(f, "2026:03:25 10:00:00", false))
	f.Add(buildJPEG(f, "2026:03:25 10:00:00", true))
	f.Add(buildTIFF(f, "2026:03:25 10:00:00", binary.LittleEndian))
	f.Add(buildTIFF(f, "2026:03:25 10:00:00", binary.BigEndian))
	f.Add(buildRAF(f, "2026:03:25 10:00:00"))
	f.Add(buildMOV(time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)))
	f.Add(buildHEIF(f, "2026:03:25 10:00:00", heifOptions{}))

	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "fuzz.bin")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		// The result does not matter — surviving the input does.
		CaptureTime(path)
	})
}
