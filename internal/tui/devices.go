package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Eric-Eklund/camera-backup/internal/config"
	"github.com/Eric-Eklund/camera-backup/internal/devices"
)

// pickerMode is what selecting a device in the picker does.
type pickerMode int

const (
	// pickerSwap makes the chosen device the source for this run and
	// immediately rescans it against the NAS (and the SSD, when it is in use).
	pickerSwap pickerMode = iota
	// pickerField writes the chosen path into a settings field, so a path can
	// be picked from a list instead of typed.
	pickerField
)

// devicePicker is the state of the device screen: what discovery found, which
// row is focused, and what a selection should do.
type devicePicker struct {
	mode    pickerMode
	devs    []devices.Device
	cursor  int
	loading bool
	err     string
	notice  string
	// active is the source path in force right now, marked in the list so the
	// user can see what they are about to swap away from.
	active string
	// fieldLabel names the settings row being filled (pickerField only).
	fieldLabel string
}

func newDevicePicker(mode pickerMode, active, fieldLabel string) *devicePicker {
	return &devicePicker{mode: mode, loading: true, active: active, fieldLabel: fieldLabel}
}

func (p *devicePicker) moveCursor(delta int) {
	p.cursor += delta
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.devs) {
		p.cursor = len(p.devs) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// current returns the focused device, or false when the list is empty.
func (p *devicePicker) current() (devices.Device, bool) {
	if p.cursor < 0 || p.cursor >= len(p.devs) {
		return devices.Device{}, false
	}
	return p.devs[p.cursor], true
}

// apply stores a finished scan, keeping the cursor on the active device so the
// list opens on what is in use and a refresh does not move the focus around.
func (p *devicePicker) apply(devs []devices.Device, err error) {
	p.loading = false
	p.devs = devs
	if err != nil {
		p.err = err.Error()
		return
	}
	p.err = ""
	for i, d := range devs {
		if d.Path == p.active {
			p.cursor = i
			return
		}
	}
	if p.cursor >= len(devs) {
		p.cursor = 0
	}
}

// scanDevicesCmd lists mounted filesystems in the background: discovery stats
// every mount point, which a dead network share can make slow.
func scanDevicesCmd() tea.Cmd {
	return func() tea.Msg {
		devs, err := devices.List()
		return devicesReadyMsg{devs: devs, err: err}
	}
}

// useDevice makes d the source for this run and kicks off the rescan. Nothing
// is written to config.toml: swapping cards is a thing that happens several
// times an afternoon, and the configured devices should still be there when
// the card comes out.
func (m *Model) useDevice(d devices.Device) tea.Cmd {
	m.cfg.SetSourceOverride(d.Path)
	// The previous device's files are gone from the tree the moment the scan
	// lands, so a selection carried over would silently apply to nothing.
	m.selected = map[string]bool{}
	m.screen = screenMain
	m.picker = nil
	m.statusMsg = fmt.Sprintf("Reading %s — comparing against %s…", d.Name(), destinationsInUse(m.cfg))
	m.logger.Printf("source device switched to %s (%s)", d.Path, d.Node)
	return tea.Batch(m.restartWatcher(), statusScanCmd(m.cfg, m.logger))
}

// saveDeviceAsSource writes the picked device into config.toml as `source`,
// for the card reader that is always in the same slot. The path that source
// held moves into extra_sources rather than being dropped — the old device is
// still a device the user backs up from.
func (m *Model) saveDeviceAsSource(d devices.Device) tea.Cmd {
	if m.configPath == "" {
		m.picker.err = "started without a config file — cannot save"
		return nil
	}
	draft := *m.cfg
	if draft.Source != "" && draft.Source != d.Path {
		draft.ExtraSources = appendUnique(draft.ExtraSources, draft.Source)
	}
	draft.Source = d.Path
	draft.ExtraSources = removePath(draft.ExtraSources, d.Path)
	// config.toml now says what the source is, so the runtime override would
	// only be a second, invisible answer to the same question.
	draft.SetSourceOverride("")

	if err := draft.Validate(); err != nil {
		m.picker.err = err.Error()
		return nil
	}
	if err := draft.Save(m.configPath); err != nil {
		m.picker.err = err.Error()
		return nil
	}

	m.cfg = &draft
	m.selected = map[string]bool{}
	m.picker.err = ""
	m.picker.notice = fmt.Sprintf("%s saved as source in %s", d.Name(), m.configPath)
	m.picker.active = d.Path
	m.statusMsg = "Rescanning after config change…"
	m.logger.Printf("source saved to config: %s", d.Path)
	return tea.Batch(m.restartWatcher(), statusScanCmd(m.cfg, m.logger))
}

// destinationsInUse names what a fresh scan will compare the source against,
// so the status line says what is actually being checked.
func destinationsInUse(cfg *config.Config) string {
	switch {
	case cfg.SSDInUse() && cfg.NASConfigured():
		return "SSD and NAS"
	case cfg.SSDInUse():
		return "the SSD"
	case cfg.NASConfigured():
		return "the NAS"
	}
	return "the configured destinations"
}

func appendUnique(list []string, path string) []string {
	for _, p := range list {
		if p == path {
			return list
		}
	}
	return append(list, path)
}

func removePath(list []string, path string) []string {
	out := list[:0:0]
	for _, p := range list {
		if p != path {
			out = append(out, p)
		}
	}
	return out
}
