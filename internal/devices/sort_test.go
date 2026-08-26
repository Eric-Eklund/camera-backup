package devices

import "testing"

func TestSortDevices_CardFirstThenHotplug(t *testing.T) {
	ds := []Device{
		{Path: "/", Kind: KindInternal},
		{Path: "/mnt/nas", Kind: KindNetwork},
		{Path: "/run/media/eric/EXT-SSD", Kind: KindInternal}, // eSATA dock: no removable flag
		{Path: "/run/media/eric/CAMERA-CARD", Kind: KindRemovable, HasDCIM: true},
		{Path: "/media/backup", Kind: KindRemovable},
	}
	sortDevices(ds)

	want := []string{
		"/run/media/eric/CAMERA-CARD", // holds DCIM
		"/media/backup",               // hot-plugged, removable
		"/run/media/eric/EXT-SSD",     // hot-plugged
		"/mnt/nas",                    // share
		"/",                           // fixed disk
	}
	for i, w := range want {
		if ds[i].Path != w {
			t.Errorf("position %d = %q, want %q (order: %v)", i, ds[i].Path, w, paths(ds))
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[uint64]string{
		512:                    "512 B",
		2048:                   "2.0 KB",
		5 * 1024 * 1024:        "5.0 MB",
		3 * 1024 * 1024 * 1024: "3.0 GB",
	}
	for in, want := range cases {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDeviceName_PrefersLabel(t *testing.T) {
	if got := (Device{Path: "/run/media/eric/disk1", Label: "CAMERA-CARD"}).Name(); got != "CAMERA-CARD" {
		t.Errorf("Name() = %q, want the label", got)
	}
	if got := (Device{Path: "/run/media/eric/disk1"}).Name(); got != "disk1" {
		t.Errorf("Name() = %q, want the mount point's last element", got)
	}
	if got := (Device{Path: "/"}).Name(); got != "/" {
		t.Errorf("Name() = %q, want %q", got, "/")
	}
}

func paths(ds []Device) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Path
	}
	return out
}
