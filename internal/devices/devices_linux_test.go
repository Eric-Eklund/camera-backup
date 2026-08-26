//go:build linux

package devices

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMountInfo(t *testing.T) {
	const in = `23 28 0:22 / /proc rw,nosuid - proc proc rw
36 25 8:17 / /run/media/eric/CAMERA-CARD rw,nosuid,nodev shared:1 master:2 - vfat /dev/sdb1 rw,uid=1000
41 25 0:56 / /mnt/nas\040share rw,relatime - cifs //nas/photos rw
too short
44 25 0:57 / /broken rw,relatime shared:9 no-separator-here cifs //x/y rw
`
	got := parseMountInfo(strings.NewReader(in))
	want := []mountEntry{
		{point: "/proc", fsType: "proc", source: "proc"},
		{point: "/run/media/eric/CAMERA-CARD", fsType: "vfat", source: "/dev/sdb1"},
		{point: "/mnt/nas share", fsType: "cifs", source: "//nas/photos"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestUnescape(t *testing.T) {
	cases := map[string]string{
		`/mnt/plain`:         "/mnt/plain",
		`/mnt/two\040words`:  "/mnt/two words",
		`/mnt/tab\011here`:   "/mnt/tab\there",
		`/mnt/back\134slash`: `/mnt/back\slash`,
		`/mnt/not\09digits`:  `/mnt/not\09digits`,
		`/mnt/trailing\04`:   `/mnt/trailing\04`,
	}
	for in, want := range cases {
		if got := unescape(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeTree builds the bits of /sys and /dev that discovery reads, mirroring the
// real layout: /sys/class/block/<part> is a symlink into /sys/devices, and the
// removable flag lives on the parent disk.
type fakeTree struct {
	root        string
	sysClassBlk string
	byLabel     string
}

func newFakeTree(t *testing.T) *fakeTree {
	t.Helper()
	root := t.TempDir()
	f := &fakeTree{
		root:        root,
		sysClassBlk: filepath.Join(root, "sys", "class", "block"),
		byLabel:     filepath.Join(root, "dev", "disk", "by-label"),
	}
	mkdir(t, f.sysClassBlk)
	mkdir(t, f.byLabel)
	return f
}

// addDisk registers a disk with one partition at the given /sys/devices path,
// e.g. "pci0000:00/usb1" or "pci0000:00/ata1".
func (f *fakeTree) addDisk(t *testing.T, busPath, disk, part string, removable bool) {
	t.Helper()
	diskDir := filepath.Join(f.root, "sys", "devices", busPath, "block", disk)
	mkdir(t, filepath.Join(diskDir, part))
	flag := "0\n"
	if removable {
		flag = "1\n"
	}
	write(t, filepath.Join(diskDir, "removable"), flag)
	if err := os.Symlink(filepath.Join(diskDir, part), filepath.Join(f.sysClassBlk, part)); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeTree) addLabel(t *testing.T, label, part string) {
	t.Helper()
	node := filepath.Join(f.root, "dev", part)
	write(t, node, "")
	if err := os.Symlink(node, filepath.Join(f.byLabel, label)); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeTree) lister(t *testing.T, mountInfo string) *linuxLister {
	t.Helper()
	path := filepath.Join(f.root, "mountinfo")
	write(t, path, mountInfo)
	return &linuxLister{
		mountInfo:    path,
		sysClassBlk:  f.sysClassBlk,
		diskByLabel:  f.byLabel,
		probeTimeout: 2 * time.Second,
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestList_ClassifiesAndOrders(t *testing.T) {
	f := newFakeTree(t)
	f.addDisk(t, "pci0000:00/usb1/1-2", "sdb", "sdb1", true)  // card reader
	f.addDisk(t, "pci0000:00/ata1", "sda", "sda2", false)     // internal disk
	f.addDisk(t, "pci0000:00/usb1/1-4", "sdc", "sdc1", false) // USB drive, flag not set
	f.addLabel(t, "CAMERA-CARD", "sdb1")
	f.addLabel(t, "EXT-SSD", "sdc1")

	card := t.TempDir()
	mkdir(t, filepath.Join(card, "DCIM"))
	ssd := t.TempDir()
	rootfs := t.TempDir()
	nas := t.TempDir()

	mountInfo := fmt.Sprintf(`23 28 0:22 / /proc rw - proc proc rw
24 28 0:23 / /sys rw - sysfs sysfs rw
25 28 0:24 / /snap/core rw - squashfs /dev/loop0 ro
26 28 8:2 / %s rw shared:1 - ext4 /dev/sda2 rw
36 26 8:17 / %s rw,nosuid shared:2 master:3 - vfat /dev/sdb1 rw
37 26 8:33 / %s rw - ext4 /dev/sdc1 rw
41 26 8:2 / %s rw - ext4 /dev/sda2 rw
42 26 0:56 / %s rw - cifs //nas/photos rw
`, rootfs, card, ssd, nas, nas)

	got, err := f.lister(t, mountInfo).list()
	if err != nil {
		t.Fatal(err)
	}

	byPath := map[string]Device{}
	for _, d := range got {
		byPath[d.Path] = d
	}
	if len(got) != 4 {
		t.Fatalf("listed %d devices, want 4 (pseudo filesystems dropped, the shared mount point counted once): %v", len(got), got)
	}

	if d := byPath[card]; d.Kind != KindRemovable || d.Label != "CAMERA-CARD" || !d.HasDCIM {
		t.Errorf("card = %+v, want removable CAMERA-CARD with DCIM", d)
	}
	if d := byPath[ssd]; d.Kind != KindRemovable || d.Label != "EXT-SSD" {
		t.Errorf("USB SSD = %+v, want removable EXT-SSD (bus, not the removable flag)", d)
	}
	if d := byPath[rootfs]; d.Kind != KindInternal {
		t.Errorf("root filesystem = %+v, want internal", d)
	}
	// Two filesystems on one mount point: the second is mounted over the first,
	// so it is the one reachable through that path.
	if d := byPath[nas]; d.Kind != KindNetwork || d.Node != "//nas/photos" {
		t.Errorf("share = %+v, want the overmounted network share //nas/photos", d)
	}
	if byPath[rootfs].TotalBytes == 0 {
		t.Error("root filesystem has no size — statfs was not applied")
	}

	// The card carries DCIM, so it sorts ahead of everything; internal disks last.
	if got[0].Path != card {
		t.Errorf("first device = %q, want the card %q", got[0].Path, card)
	}
	if got[len(got)-1].Path != rootfs {
		t.Errorf("last device = %q, want the internal disk %q", got[len(got)-1].Path, rootfs)
	}
}

// A file can be a mount point of its own — a bind mount of /etc/hostname, of a
// sysfs attribute — and mountinfo lists it like any other mount. It can never
// be a source device.
func TestList_SkipsFileMountPoints(t *testing.T) {
	f := newFakeTree(t)
	f.addDisk(t, "pci0000:00/ata1", "sda", "sda2", false)
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), "hostname")
	write(t, file, "nikon\n")

	info := fmt.Sprintf("36 25 8:2 / %s rw - ext4 /dev/sda2 rw\n37 25 8:2 /etc/hostname %s rw - ext4 /dev/sda2 rw\n", dir, file)
	got, err := f.lister(t, info).list()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != dir {
		t.Fatalf("got %+v, want only the directory mount %q", got, dir)
	}
}

func TestList_SDSlotIsRemovable(t *testing.T) {
	f := newFakeTree(t)
	// A PCIe SD reader: the disk reports removable = 0, and only the mmc_host
	// in its device path says it is a card slot.
	f.addDisk(t, "pci0000:00/0000:01:00.0/mmc_host/mmc0", "mmcblk0", "mmcblk0p1", false)
	mount := t.TempDir()
	info := fmt.Sprintf("36 25 179:1 / %s rw - vfat /dev/mmcblk0p1 rw\n", mount)

	got, err := f.lister(t, info).list()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != KindRemovable {
		t.Fatalf("got %+v, want one removable device", got)
	}
}

func TestList_UnknownBlockDeviceStillListed(t *testing.T) {
	f := newFakeTree(t)
	mount := t.TempDir()
	info := fmt.Sprintf("36 25 8:1 / %s rw - ext4 /dev/nowhere1 rw\n", mount)

	got, err := f.lister(t, info).list()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != KindUnknown {
		t.Fatalf("got %+v, want one device of unknown kind", got)
	}
	if got[0].Name() != filepath.Base(mount) {
		t.Errorf("Name() = %q, want the mount point's last element", got[0].Name())
	}
}
