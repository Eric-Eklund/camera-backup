//go:build !linux

package devices

// List is Linux-only: discovery reads /proc/self/mountinfo and /sys/class/block.
// The stub keeps the package compiling for a cross-build; every caller treats an
// empty list as "nothing to pick from" and falls back to typing a path.
func List() ([]Device, error) { return nil, nil }
