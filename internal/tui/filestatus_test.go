package tui

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/scan"
	"github.com/Eric-Eklund/camera-backup/internal/status"
	"github.com/Eric-Eklund/camera-backup/internal/verify"
)

// modelWith builds a model in the state statusReadyMsg leaves it in, so the
// markers under test are derived exactly as they are at runtime.
func modelWith(r *status.StatusResult) *Model {
	m := &Model{
		cfg:        &config.Config{VideoExtensions: []string{".MOV"}},
		status:     r,
		missingSSD: absPathSet(r.MissingOnSSD),
		missingNAS: absPathSet(r.MissingOnNAS),
	}
	return m
}

// The marker means "the copy would not copy this", nothing looser. A
// destination file that merely shares the name is not the same photograph, and
// showing a tick for it told the user a file was safe when the next run was
// about to copy it as a _1 variant.
func TestFileStatus_AFileThatWouldStillBeCopiedIsNotTicked(t *testing.T) {
	onCard := srcFile("DCIM/100NIKON/DSC_0003.NEF", 1<<20)
	alreadyThere := srcFile("DCIM/100NIKON/DSC_0002.NEF", 512<<10)

	m := modelWith(&status.StatusResult{
		SourceAvail:  true,
		CameraFiles:  []scan.FileInfo{alreadyThere, onCard},
		MissingOnSSD: []scan.FileInfo{onCard},
		MissingOnNAS: []scan.FileInfo{onCard},
	})

	if ssd, nas := m.fileStatus(onCard); ssd || nas {
		t.Errorf("fileStatus(still to copy) = ssd:%v nas:%v, want both false", ssd, nas)
	}
	if ssd, nas := m.fileStatus(alreadyThere); !ssd || !nas {
		t.Errorf("fileStatus(already copied) = ssd:%v nas:%v, want both true", ssd, nas)
	}
}

// The two destinations are tracked apart: a file on the SSD but not yet on the
// NAS is exactly the state between the two phases of a copy.
func TestFileStatus_TracksTheTwoDestinationsSeparately(t *testing.T) {
	f := srcFile("DCIM/100NIKON/DSC_0001.NEF", 1<<20)
	m := modelWith(&status.StatusResult{
		SourceAvail:  true,
		CameraFiles:  []scan.FileInfo{f},
		MissingOnSSD: nil,
		MissingOnNAS: []scan.FileInfo{f},
	})

	ssd, nas := m.fileStatus(f)
	if !ssd {
		t.Error("want ✓SSD — the file is not in MissingOnSSD")
	}
	if nas {
		t.Error("want ✗NAS — the file is in MissingOnNAS")
	}
}

// A copy already filed under another date is recognised by MissingFromDest and
// will not be copied again, so it must show as backed up. The old
// filename-set lookup marked it missing, the mirror image of the bug above.
func TestFileStatus_AFileFiledUnderAnotherDateStillCountsAsBackedUp(t *testing.T) {
	f := srcFile("DCIM/100NIKON/DSC_0007.NEF", 900)
	m := modelWith(&status.StatusResult{
		SourceAvail: true,
		CameraFiles: []scan.FileInfo{f},
		// Neither list contains it: the scan found the copy elsewhere in the
		// tree and confirmed it by capture time.
	})

	if ssd, nas := m.fileStatus(f); !ssd || !nas {
		t.Errorf("fileStatus = ssd:%v nas:%v, want both true — the copy exists under another date", ssd, nas)
	}
}

// The counts on the tabs and the markers in the list come from one source, so
// they cannot disagree — which is how the discrepancy was noticed: the tab
// said six missing while only five rows carried a ✗.
func TestFileStatus_AgreesWithTheMissingCount(t *testing.T) {
	var all, missing []scan.FileInfo
	for _, n := range []string{"A", "B", "C", "D", "E"} {
		f := srcFile("DCIM/100NIKON/DSC_000"+n+".NEF", 1<<20)
		all = append(all, f)
		if n == "B" || n == "D" {
			missing = append(missing, f)
		}
	}
	m := modelWith(&status.StatusResult{SourceAvail: true, CameraFiles: all, MissingOnSSD: missing})

	notTicked := 0
	for _, f := range all {
		if ssd, _ := m.fileStatus(f); !ssd {
			notTicked++
		}
	}
	if notTicked != len(missing) {
		t.Errorf("%d files show ✗SSD but %d are counted as missing", notTicked, len(missing))
	}
}

// Before the first scan the model has no answer, and must not invent an
// encouraging one.
func TestFileStatus_IsBlankBeforeTheFirstScan(t *testing.T) {
	m := &Model{cfg: &config.Config{}}
	if ssd, nas := m.fileStatus(srcFile("DSC_0001.NEF", 1)); ssd || nas {
		t.Errorf("fileStatus with no status = ssd:%v nas:%v, want both false", ssd, nas)
	}
}

func TestAbsPathSet(t *testing.T) {
	set := absPathSet([]scan.FileInfo{
		srcFile("DCIM/100NIKON/DSC_0001.NEF", 1),
		srcFile("DCIM/101NIKON/DSC_0001.NEF", 1),
	})
	if len(set) != 2 {
		t.Fatalf("set = %v, want two entries — same basename, different cards", set)
	}
	if !set["/cam/DCIM/101NIKON/DSC_0001.NEF"] {
		t.Error("the second file is missing from the set")
	}
}

// ── The verify result on the done screen ────────────────────────────────────

func TestVerifyDoneText(t *testing.T) {
	unreadable := []scan.Unreadable{{Path: "/card/DCIM/101NIKON", Err: fs.ErrPermission}}

	tests := []struct {
		name     string
		msg      verifyDoneMsg
		contains []string
		absent   []string
	}{
		{
			name:     "a clean pass says so plainly",
			msg:      verifyDoneMsg{bad: 0, total: 47},
			contains: []string{"All 47 files verified OK."},
		},
		{
			name: "an unmounted destination is named",
			msg: verifyDoneMsg{bad: 0, total: 47,
				outcome: verify.Outcome{UnmountedRoots: []string{"NAS photos (/mnt/nas/Photos)"}}},
			contains: []string{"what could be checked", "Not checked: NAS photos (/mnt/nas/Photos)"},
			absent:   []string{"All 47 files verified OK."},
		},
		{
			name: "an unreadable source path is named and warned about",
			msg: verifyDoneMsg{bad: 0, total: 47,
				outcome: verify.Outcome{Unreadable: unreadable}},
			contains: []string{"could NOT be read", "Do not format the card", "/card/DCIM/101NIKON"},
			absent:   []string{"All 47 files verified OK."},
		},
		{
			name:     "failures are reported with the count",
			msg:      verifyDoneMsg{bad: 2, total: 47},
			contains: []string{"2 / 47 files have issues."},
		},
		{
			name: "everything at once",
			msg: verifyDoneMsg{bad: 2, total: 47, outcome: verify.Outcome{
				UnmountedRoots: []string{"NAS (/mnt/nas)"},
				Unreadable:     unreadable,
			}},
			contains: []string{"2 / 47 files have issues.", "Not checked: NAS (/mnt/nas)", "could NOT be read"},
		},
		{
			name:     "a pass that could not run at all",
			msg:      verifyDoneMsg{bad: -1},
			contains: []string{"Verify failed to run"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyDoneText(tc.msg)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("text %q does not contain %q", got, want)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(got, unwanted) {
					t.Errorf("text %q should not contain %q", got, unwanted)
				}
			}
		})
	}
}
