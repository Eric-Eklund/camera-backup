//go:build linux

package devices

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Default locations of everything the discovery reads. They are fields on
// linuxLister rather than constants so the tests can point the whole lot at a
// fake /proc and /sys tree.
const (
	defaultMountInfo    = "/proc/self/mountinfo"
	defaultSysClassBlk  = "/sys/class/block"
	defaultDiskByLabel  = "/dev/disk/by-label"
	defaultProbeTimeout = 2 * time.Second
)

// localFSTypes are the filesystems a camera card, a USB stick or a fixed disk
// can plausibly be formatted with. An allowlist, not a denylist: the kernel
// mounts dozens of pseudo filesystems (proc, sysfs, cgroup, overlay, the
// squashfs behind every snap) that are never a place photographs live, and new
// ones keep appearing.
var localFSTypes = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true,
	"xfs": true, "btrfs": true, "f2fs": true, "jfs": true, "reiserfs": true,
	"zfs": true, "bcachefs": true,
	"vfat": true, "msdos": true, "exfat": true,
	"ntfs": true, "ntfs3": true, "fuseblk": true, // fuseblk: ntfs-3g, exfat-fuse
	"hfs": true, "hfsplus": true, "apfs": true,
	"iso9660": true, "udf": true,
}

// networkFSTypes are shares — a NAS reached over SMB, NFS or SSH.
var networkFSTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true,
	"fuse.sshfs": true, "ceph": true, "glusterfs": true, "afs": true, "9p": true,
}

// List returns every mounted filesystem that could hold media, ordered with the
// most likely source device first. Unreadable or unprobeable devices are
// reported with zero sizes rather than dropped — a share that does not answer
// statfs is still a mount point the user may want to pick.
func List() ([]Device, error) {
	l := &linuxLister{
		mountInfo:    defaultMountInfo,
		sysClassBlk:  defaultSysClassBlk,
		diskByLabel:  defaultDiskByLabel,
		probeTimeout: defaultProbeTimeout,
	}
	return l.list()
}

type linuxLister struct {
	mountInfo    string
	sysClassBlk  string
	diskByLabel  string
	probeTimeout time.Duration
}

func (l *linuxLister) list() ([]Device, error) {
	f, err := os.Open(l.mountInfo)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	mounts := parseMountInfo(f)
	labels := l.labelsByDevice()

	devs := make([]Device, 0, len(mounts))
	// mountinfo is in mount order, so when two filesystems share a mount point
	// the later one is the one actually visible there — it is mounted over the
	// first. Keeping the earlier entry would name a filesystem nothing can
	// read through that path.
	at := map[string]int{}
	for _, mt := range mounts {
		var kind Kind
		switch {
		case networkFSTypes[mt.fsType]:
			kind = KindNetwork
		case localFSTypes[mt.fsType]:
			kind = l.blockKind(mt.source)
		default:
			continue
		}
		d := Device{
			Path:   mt.point,
			Label:  labels[filepath.Base(mt.source)],
			Node:   mt.source,
			FSType: mt.fsType,
			Kind:   kind,
		}
		if i, dup := at[mt.point]; dup {
			devs[i] = d
			continue
		}
		at[mt.point] = len(devs)
		devs = append(devs, d)
	}

	l.probeAll(devs)
	devs = dropNonDirs(devs)
	sortDevices(devs)
	return devs, nil
}

// probeAll fills in free space and the DCIM marker. Both touch the filesystem,
// which on a dead network mount blocks until the kernel gives up — minutes, or
// forever with a hard NFS mount. Each device therefore gets its own goroutine
// and a deadline, and a device that misses it is simply listed without sizes.
func (l *linuxLister) probeAll(devs []Device) {
	var wg sync.WaitGroup
	for i := range devs {
		wg.Add(1)
		go func(d *Device) {
			defer wg.Done()
			// The probe works from its own copy and reads nothing through d.
			// When it misses the deadline this goroutine returns, but the
			// probe itself runs on — and by then the caller is compacting and
			// sorting the very slice d points into.
			start := *d
			done := make(chan Device, 1)
			go func() {
				probed := start
				if fs, err := statfs(probed.Path); err == nil {
					probed.TotalBytes = fs.total
					probed.FreeBytes = fs.free
				}
				if st, err := os.Stat(probed.Path); err == nil && !st.IsDir() {
					probed.notDir = true
				}
				if st, err := os.Stat(filepath.Join(probed.Path, "DCIM")); err == nil && st.IsDir() {
					probed.HasDCIM = true
				}
				done <- probed
			}()
			select {
			case probed := <-done:
				*d = probed
			case <-time.After(l.probeTimeout):
			}
		}(&devs[i])
	}
	wg.Wait()
}

// dropNonDirs removes mount points that turned out not to be directories. A
// device whose probe timed out keeps its place: an unresponsive share is far
// more likely to be a directory than a bind-mounted file.
func dropNonDirs(devs []Device) []Device {
	out := devs[:0]
	for _, d := range devs {
		if !d.notDir {
			out = append(out, d)
		}
	}
	return out
}

// blockKind decides how a block device is attached. A card reader is what
// matters here, and there are three ways one shows up: the disk reports itself
// removable, it hangs off a USB controller, or it is an SD slot on the PCI bus
// (mmcblk, whose disks report removable = 0).
func (l *linuxLister) blockKind(devNode string) Kind {
	name := filepath.Base(devNode)
	if name == "" || !strings.HasPrefix(devNode, "/dev/") {
		return KindUnknown
	}
	sysPath, err := filepath.EvalSymlinks(filepath.Join(l.sysClassBlk, name))
	if err != nil {
		return KindUnknown
	}
	if readsOne(filepath.Join(sysPath, "removable")) ||
		readsOne(filepath.Join(sysPath, "..", "removable")) {
		return KindRemovable
	}
	// EvalSymlinks has resolved this into the /sys/devices tree, so the bus the
	// disk sits on is visible in the path itself.
	if strings.Contains(sysPath, "/usb") || strings.Contains(sysPath, "/mmc_host/") {
		return KindRemovable
	}
	return KindInternal
}

// readsOne reports whether a sysfs flag file contains 1.
func readsOne(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// labelsByDevice maps a device node's base name ("sdb1") to its filesystem
// label. /dev/disk/by-label holds one symlink per labeled filesystem, which is
// the same source the mount point under /run/media is named after.
func (l *linuxLister) labelsByDevice() map[string]string {
	entries, err := os.ReadDir(l.diskByLabel)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		target, err := filepath.EvalSymlinks(filepath.Join(l.diskByLabel, e.Name()))
		if err != nil {
			continue
		}
		// udev escapes bytes that are awkward in a filename the same way
		// mountinfo does.
		out[filepath.Base(target)] = unescape(e.Name())
	}
	return out
}

type mountEntry struct {
	point  string
	fsType string
	source string
}

// parseMountInfo reads /proc/self/mountinfo:
//
//	36 35 98:0 /root /mnt/point rw,noatime shared:1 - ext4 /dev/sdb1 rw
//	                  ^ 5th field           optional ^ ^ after the separator
//
// The variable number of optional fields before the "-" is why this file is
// parsed instead of /proc/mounts — that one cannot represent a mount point
// containing a space unambiguously, and udisks mount points are named after
// volume labels, which very much can contain spaces.
func parseMountInfo(r io.Reader) []mountEntry {
	var out []mountEntry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+2 >= len(fields) {
			continue
		}
		out = append(out, mountEntry{
			point:  unescape(fields[4]),
			fsType: fields[sep+1],
			source: unescape(fields[sep+2]),
		})
	}
	return out
}

// unescape decodes the octal escapes the kernel and udev use for characters
// that would otherwise break a whitespace-separated field.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, ok := octal3(s[i+1 : i+4]); ok {
				sb.WriteByte(v)
				i += 3
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func octal3(s string) (byte, bool) {
	var v int
	for i := 0; i < 3; i++ {
		if s[i] < '0' || s[i] > '7' {
			return 0, false
		}
		v = v*8 + int(s[i]-'0')
	}
	if v > 255 {
		return 0, false
	}
	return byte(v), true
}

type fsSize struct{ total, free uint64 }

func statfs(path string) (fsSize, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return fsSize{}, err
	}
	bs := uint64(st.Bsize)
	return fsSize{total: st.Blocks * bs, free: st.Bavail * bs}, nil
}
