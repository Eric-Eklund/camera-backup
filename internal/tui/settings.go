package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Eric-Eklund/camera-backup/internal/config"
)

// fieldKind describes how one settings row is edited.
type fieldKind int

const (
	fieldText fieldKind = iota // free text (a single path)
	fieldList                  // comma-separated list
	fieldBool                  // on/off toggle
	fieldInt                   // positive number
	fieldEnum                  // cycles through choices
)

// pathCheck says how a field's value is probed for the live ✔/✘ marker.
// Destination roots are treated like the copy code treats them: a root counts
// as available when it or its parent exists, because it is created on first
// copy. A source must already be there — an empty mount point would otherwise
// look like an empty card.
type pathCheck int

const (
	pathNone pathCheck = iota
	pathSource
	pathRoot
	pathSourceList
)

const (
	boolOn  = "on"
	boolOff = "off"
)

// settingsField is one editable row.
type settingsField struct {
	label   string
	key     string // TOML key, shown as a dim hint
	kind    fieldKind
	value   string
	choices []string // fieldEnum only
	path    pathCheck
	hint    string
	// optional marks a field that may be left empty (SSD roots in direct mode,
	// the NAS pair, extra sources). Required fields flag an empty value early
	// instead of failing on save.
	optional bool
}

// settingsForm is the state of the settings screen: the editable copy of the
// config, which row is focused, and whether a row is being typed into.
type settingsForm struct {
	configPath string
	fields     []settingsField
	cursor     int
	editing    bool
	editor     lineEditor
	editOrig   string // value to restore when an edit is cancelled
	dirty      bool
	err        string // validation or save failure
	notice     string // transient success message
	// confirmExit is set after the first exit attempt with unsaved changes, so
	// leaving without saving takes a deliberate second keypress.
	confirmExit bool
}

// newSettingsForm builds the form from the running config. Optional numeric and
// enum keys show their *effective* value, so the screen never displays a blank
// for a setting that is actually in force.
func newSettingsForm(cfg *config.Config, configPath string) *settingsForm {
	return &settingsForm{
		configPath: configPath,
		fields: []settingsField{
			{
				label: "Source device", key: "source", kind: fieldText,
				value: cfg.Source, path: pathSource,
				hint: "mount point of the card reader or drive",
			},
			{
				label: "Extra sources", key: "extra_sources", kind: fieldList,
				value: strings.Join(cfg.ExtraSources, ", "), path: pathSourceList,
				hint: "tried in order when the source above is not mounted", optional: true,
			},
			{
				label: "Direct to NAS", key: "direct_to_nas", kind: fieldBool,
				value: boolText(cfg.DirectToNAS),
				hint:  "bypass the local SSD — dump straight to the NAS, verified",
			},
			{
				label: "SSD photos", key: "ssd_photos", kind: fieldText,
				value: cfg.SSDPhotos, path: pathRoot,
				hint: "same path as SSD videos merges the two categories", optional: true,
			},
			{
				label: "SSD videos", key: "ssd_videos", kind: fieldText,
				value: cfg.SSDVideos, path: pathRoot, optional: true,
			},
			{
				label: "NAS photos", key: "nas_photos", kind: fieldText,
				value: cfg.NASPhotos, path: pathRoot,
				hint: "set both NAS roots or neither", optional: true,
			},
			{
				label: "NAS videos", key: "nas_videos", kind: fieldText,
				value: cfg.NASVideos, path: pathRoot, optional: true,
			},
			{
				label: "File extensions", key: "file_extensions", kind: fieldList,
				value: strings.Join(cfg.FileExtensions, ", "),
				hint:  "what counts as media; a missing dot is added for you",
			},
			{
				label: "Video extensions", key: "video_extensions", kind: fieldList,
				value: strings.Join(cfg.VideoExtensions, ", "),
				hint:  "these route to the videos root — everything else is photos", optional: true,
			},
			{
				label: "SSD workers", key: "ssd_workers", kind: fieldInt,
				value: strconv.Itoa(cfg.SSDWorkerCount()),
				hint:  "parallel Camera→SSD copies in the TUI",
			},
			{
				label: "NAS workers", key: "nas_workers", kind: fieldInt,
				value: strconv.Itoa(cfg.NASWorkerCount()),
				hint:  "parallel copies to the NAS in the TUI",
			},
			{
				label: "NAS write timeout", key: "nas_write_timeout_seconds", kind: fieldInt,
				value: strconv.Itoa(int(cfg.NASWriteTimeout().Seconds())),
				hint:  "seconds — a hung mount fails the file, not the batch",
			},
			{
				label: "NAS transfer order", key: "nas_sync_order", kind: fieldEnum,
				value: cfg.SyncOrder(), choices: []string{config.OrderVideosFirst, config.OrderSizeAsc},
				hint: "size-asc sends the smallest files first on a flaky link",
			},
		},
	}
}

// toConfig applies the form onto a copy of base, returning the draft to save.
// Field-level input errors (a non-numeric worker count, an empty required
// field) are reported here; cross-key rules come from config.Validate.
func (f *settingsForm) toConfig(base *config.Config) (*config.Config, error) {
	draft := *base
	for _, fl := range f.fields {
		value := strings.TrimSpace(fl.value)
		if value == "" && !fl.optional {
			return nil, fmt.Errorf("%s: cannot be empty", fl.label)
		}
		switch fl.key {
		case "source":
			draft.Source = value
		case "extra_sources":
			draft.ExtraSources = splitList(value)
		case "direct_to_nas":
			draft.DirectToNAS = value == boolOn
		case "ssd_photos":
			draft.SSDPhotos = value
		case "ssd_videos":
			draft.SSDVideos = value
		case "nas_photos":
			draft.NASPhotos = value
		case "nas_videos":
			draft.NASVideos = value
		case "file_extensions":
			draft.FileExtensions = normaliseExtList(value)
			if len(draft.FileExtensions) == 0 {
				return nil, fmt.Errorf("%s: at least one extension is required", fl.label)
			}
		case "video_extensions":
			draft.VideoExtensions = normaliseExtList(value)
		case "ssd_workers":
			n, err := parsePositive(fl.label, value, 64)
			if err != nil {
				return nil, err
			}
			draft.SSDWorkers = n
		case "nas_workers":
			n, err := parsePositive(fl.label, value, 64)
			if err != nil {
				return nil, err
			}
			draft.NASWorkers = n
		case "nas_write_timeout_seconds":
			n, err := parsePositive(fl.label, value, 86400)
			if err != nil {
				return nil, err
			}
			draft.NASWriteTimeoutSeconds = n
		case "nas_sync_order":
			draft.NASSyncOrder = value
		}
	}
	if err := draft.Validate(); err != nil {
		return nil, err
	}
	return &draft, nil
}

// ── editing ───────────────────────────────────────────────────────────────────

// startEdit puts the focused text-like field into edit mode. Toggles and enums
// are changed in place instead, so they never enter the editor.
func (f *settingsForm) startEdit() {
	fl := &f.fields[f.cursor]
	switch fl.kind {
	case fieldBool:
		f.setValue(toggleBool(fl.value))
	case fieldEnum:
		f.setValue(nextChoice(fl.choices, fl.value))
	default:
		f.editing = true
		f.editOrig = fl.value
		f.editor = newLineEditor(fl.value)
	}
}

// commitEdit stores the edited text on the focused field.
func (f *settingsForm) commitEdit() {
	f.editing = false
	f.setValue(f.editor.String())
}

// cancelEdit throws the edit away, restoring the value the field had.
func (f *settingsForm) cancelEdit() {
	f.editing = false
	f.fields[f.cursor].value = f.editOrig
}

// setValue records a change and clears stale messages so the footer always
// describes the current state.
func (f *settingsForm) setValue(v string) {
	if f.fields[f.cursor].value == v {
		return
	}
	f.fields[f.cursor].value = v
	f.dirty = true
	f.err = ""
	f.notice = ""
	f.confirmExit = false
}

// canPickDevice reports whether the focused field holds a source device path,
// the only kind the device list can fill: a NAS or SSD root is a fixed mount
// point that outlives any one card.
func (f *settingsForm) canPickDevice() bool {
	switch f.fields[f.cursor].path {
	case pathSource, pathSourceList:
		return true
	}
	return false
}

// applyPickedPath writes a path chosen from the device list into the focused
// field: replacing the value of a single-path field, appending to a list
// (where a path already present is left alone rather than duplicated).
func (f *settingsForm) applyPickedPath(path string) {
	fl := f.fields[f.cursor]
	if fl.kind != fieldList {
		f.setValue(path)
		return
	}
	items := splitList(fl.value)
	for _, it := range items {
		if it == path {
			return
		}
	}
	f.setValue(strings.Join(append(items, path), ", "))
}

func (f *settingsForm) moveCursor(delta int) {
	f.cursor += delta
	if f.cursor < 0 {
		f.cursor = 0
	}
	if f.cursor >= len(f.fields) {
		f.cursor = len(f.fields) - 1
	}
}

func toggleBool(v string) string {
	if v == boolOn {
		return boolOff
	}
	return boolOn
}

func boolText(b bool) string {
	if b {
		return boolOn
	}
	return boolOff
}

func nextChoice(choices []string, current string) string {
	for i, c := range choices {
		if c == current {
			return choices[(i+1)%len(choices)]
		}
	}
	if len(choices) > 0 {
		return choices[0]
	}
	return current
}

// splitList parses a comma-separated list, dropping empty entries so trailing
// commas and stray spaces are harmless.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// normaliseExtList parses an extension list, adding the leading dot that
// filepath.Ext-based matching needs when the user leaves it out.
func normaliseExtList(s string) []string {
	items := splitList(s)
	for i, e := range items {
		if !strings.HasPrefix(e, ".") {
			items[i] = "." + e
		}
	}
	return items
}

func parsePositive(label, value string, max int) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", label, value)
	}
	if n < 1 || n > max {
		return 0, fmt.Errorf("%s: must be between 1 and %d", label, max)
	}
	return n, nil
}

// ── path probing ──────────────────────────────────────────────────────────────

// markerFor returns the availability indicator for a path field, probed from
// disk on every render. The value is passed in rather than read from the field
// so an in-progress edit is what gets probed — typing a path shows whether it
// exists before the edit is even accepted.
func (fl settingsField) markerFor(raw string) string {
	value := strings.TrimSpace(raw)
	switch fl.path {
	case pathSource:
		if value == "" {
			return ""
		}
		return availMark(isDir(value))
	case pathRoot:
		if value == "" {
			return styleDim.Render("unset")
		}
		return availMark(config.RootAvailable(value))
	case pathSourceList:
		paths := splitList(value)
		if len(paths) == 0 {
			return styleDim.Render("none")
		}
		mounted := 0
		for _, p := range paths {
			if isDir(p) {
				mounted++
			}
		}
		text := fmt.Sprintf("%d/%d mounted", mounted, len(paths))
		if mounted > 0 {
			return styleOK.Render(text)
		}
		return styleDim.Render(text)
	}
	return ""
}

func availMark(ok bool) string {
	if ok {
		return styleOK.Render("✔ found")
	}
	return styleErr.Render("✘ missing")
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// ── line editor ───────────────────────────────────────────────────────────────

// lineEditor is a minimal single-line text editor: a rune buffer plus a cursor.
// Hand-rolled to keep the dependency set as it is — the same reason this package
// draws its own panels and progress bars.
type lineEditor struct {
	runes []rune
	pos   int
}

func newLineEditor(s string) lineEditor {
	r := []rune(s)
	return lineEditor{runes: r, pos: len(r)}
}

func (e *lineEditor) String() string { return string(e.runes) }

func (e *lineEditor) insert(rs []rune) {
	tail := append([]rune{}, e.runes[e.pos:]...)
	e.runes = append(append(e.runes[:e.pos:e.pos], rs...), tail...)
	e.pos += len(rs)
}

func (e *lineEditor) backspace() {
	if e.pos == 0 {
		return
	}
	e.runes = append(e.runes[:e.pos-1], e.runes[e.pos:]...)
	e.pos--
}

func (e *lineEditor) deleteForward() {
	if e.pos >= len(e.runes) {
		return
	}
	e.runes = append(e.runes[:e.pos], e.runes[e.pos+1:]...)
}

func (e *lineEditor) left() {
	if e.pos > 0 {
		e.pos--
	}
}

func (e *lineEditor) right() {
	if e.pos < len(e.runes) {
		e.pos++
	}
}

func (e *lineEditor) home() { e.pos = 0 }
func (e *lineEditor) end()  { e.pos = len(e.runes) }

// clear empties the buffer — ctrl+u, for replacing a long path outright.
func (e *lineEditor) clear() {
	e.runes = nil
	e.pos = 0
}

// render draws the buffer in a window of w cells with a block cursor, scrolling
// horizontally so the cursor stays visible in a path longer than the column.
func (e *lineEditor) render(w int) string {
	if w < 1 {
		return ""
	}
	// The cursor sits *after* the last rune when appending, so the window has
	// to be able to show one cell past the end.
	start := 0
	if e.pos >= w {
		start = e.pos - w + 1
	}
	end := start + w
	if end > len(e.runes) {
		end = len(e.runes)
	}
	visible := e.runes[start:end]

	var sb strings.Builder
	for i, r := range visible {
		if start+i == e.pos {
			sb.WriteString(styleEditCursor.Render(string(r)))
			continue
		}
		sb.WriteString(styleEditText.Render(string(r)))
	}
	if e.pos >= len(e.runes) {
		sb.WriteString(styleEditCursor.Render(" "))
	}
	return sb.String()
}
