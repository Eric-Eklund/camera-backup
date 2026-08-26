// Package devices discovers the filesystems mounted on this machine so a
// source device can be picked from a list instead of typed as a path.
//
// Only source devices need this: a NAS share or a local SSD lives at a fixed
// mount point that is written once into config.toml, while the card reader or
// external drive of the day lands wherever udisks decided to mount it —
// /run/media/<user>/<VOLUME-LABEL>, which changes with the label of whatever
// card is in the slot.
package devices

import (
	"fmt"
	"sort"
	"strings"
)

// Kind is how a device is attached, which is what decides the order the picker
// shows: what the user just plugged in comes first.
type Kind int

const (
	KindUnknown   Kind = iota
	KindRemovable      // card reader, USB stick, external SSD
	KindInternal       // fixed disk in this machine
	KindNetwork        // NFS/SMB/SSHFS share
)

func (k Kind) String() string {
	switch k {
	case KindRemovable:
		return "removable"
	case KindInternal:
		return "internal"
	case KindNetwork:
		return "network"
	}
	return "unknown"
}

// Device is one mounted filesystem.
type Device struct {
	// Path is the mount point — the value that goes into config.toml.
	Path string
	// Label is the filesystem label ("CAMERA-CARD"), empty when unlabeled.
	Label string
	// Node is the backing device: /dev/sdb1 for a disk, //nas/photos or
	// host:/export for a network share.
	Node   string
	FSType string
	Kind   Kind
	// HasDCIM marks a device with a DCIM directory at its root — a memory
	// card straight out of a camera. It is a hint for ordering, not a filter:
	// an external drive holding an existing backup has no DCIM and is still a
	// perfectly good source.
	HasDCIM bool
	// TotalBytes and FreeBytes are 0 when the filesystem could not be probed
	// (a hung network mount, a permission error).
	TotalBytes, FreeBytes uint64
}

// Name is the label when the filesystem has one, otherwise the last element of
// the mount point — what the user sees in the picker.
func (d Device) Name() string {
	if d.Label != "" {
		return d.Label
	}
	if i := strings.LastIndex(d.Path, "/"); i >= 0 && i < len(d.Path)-1 {
		return d.Path[i+1:]
	}
	return d.Path
}

// UsedBytes is how much of the filesystem is occupied, 0 when unprobed.
func (d Device) UsedBytes() uint64 {
	if d.TotalBytes == 0 || d.FreeBytes > d.TotalBytes {
		return 0
	}
	return d.TotalBytes - d.FreeBytes
}

// String is a one-line description, used by the CLI listing.
func (d Device) String() string {
	parts := []string{d.Path}
	if d.Label != "" {
		parts = append(parts, "["+d.Label+"]")
	}
	parts = append(parts, d.FSType, d.Kind.String())
	if d.HasDCIM {
		parts = append(parts, "DCIM")
	}
	return strings.Join(parts, " ")
}

// FormatBytes renders a byte count the way the rest of the UI does.
func FormatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// hotplugDirs are where a desktop mounts what the user plugs in. A device
// sitting under one of them was mounted by udisks for this session, which is a
// stronger signal than any sysfs flag: a Thunderbolt or eSATA drive can report
// itself non-removable and still be the drive that arrived a minute ago.
// /mnt is deliberately not in the list — that is where fstab puts the NAS and
// the backup SSD, which are exactly what the picker should not promote.
var hotplugDirs = []string{"/run/media/", "/media/"}

// UserMounted reports whether the device was mounted into one of the desktop's
// hot-plug locations.
func (d Device) UserMounted() bool {
	for _, prefix := range hotplugDirs {
		if strings.HasPrefix(d.Path, prefix) {
			return true
		}
	}
	return false
}

// sortDevices orders the list the way a photographer looks for a card: a
// device holding a DCIM directory first, then whatever the desktop mounted for
// this session, then removable devices, network shares and fixed disks. Ties
// break on the mount point so the order is stable between scans.
func sortDevices(ds []Device) {
	rank := map[Kind]int{KindRemovable: 0, KindNetwork: 1, KindInternal: 2, KindUnknown: 3}
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.HasDCIM != b.HasDCIM {
			return a.HasDCIM
		}
		if a.UserMounted() != b.UserMounted() {
			return a.UserMounted()
		}
		if rank[a.Kind] != rank[b.Kind] {
			return rank[a.Kind] < rank[b.Kind]
		}
		return a.Path < b.Path
	})
}
