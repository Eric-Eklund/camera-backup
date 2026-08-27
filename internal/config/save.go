package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Save writes cfg back to the TOML file at path.
//
// config.toml is a hand-maintained file full of explanatory comments, so this
// is a surgical rewrite rather than a re-encode: each managed key is replaced
// on the line where it already appears (uncommenting it if it was commented
// out, and keeping any trailing comment), and only keys that appear nowhere in
// the file are appended. Unknown keys, ordering, blank lines and comments are
// left exactly as they were.
//
// The write is atomic — a temporary file in the same directory is renamed over
// the original — so an interrupted save cannot truncate a working config.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	original := ""
	if b, err := os.ReadFile(path); err == nil {
		original = string(b)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading config %q: %w", path, err)
	}

	values := c.managedValues()
	updated := applyValues(original, values)
	if err := verifyRewrite(updated, values); err != nil {
		return fmt.Errorf("refusing to write %q: %w — the file is unchanged", path, err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("creating temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %q: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %q: %w", tmpName, err)
	}
	// Match the original file's permissions; a fresh file gets 0644.
	perm := os.FileMode(0644)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing %q: %w", path, err)
	}
	return nil
}

// verifyRewrite reads the rendered file back before it is allowed to replace
// the original.
//
// Validate checks the configuration; it never sees the text Save produces, and
// a rewrite that lands in the wrong place produces a file the program cannot
// load at all — leaving the user with a tool that will not start and a config
// they did not break. So the result is parsed, and then re-rendered from what
// parsed: if every managed key comes back as the value Save meant to write,
// the rewrite hit the lines it aimed at. Anything else and nothing is written.
func verifyRewrite(rendered string, want []keyValue) error {
	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		return fmt.Errorf("the rewrite produced invalid TOML (%v)", err)
	}
	roundTripped := got.managedValues()
	if len(roundTripped) != len(want) {
		return fmt.Errorf("the rewrite produced %d managed keys, expected %d", len(roundTripped), len(want))
	}
	for i, kv := range want {
		if roundTripped[i] != kv {
			return fmt.Errorf("the rewrite did not take effect for %q (wrote %s, read back %s)",
				kv.key, kv.value, roundTripped[i].value)
		}
	}
	return nil
}

// keyValue is one TOML assignment to write.
type keyValue struct {
	key   string
	value string // already TOML-formatted
}

// managedValues returns every key Save owns, in the order they are appended to
// a file that does not have them yet.
//
// Optional keys are written as explicit values rather than left to their
// defaults: the settings screen shows effective values, so what the user sees
// is what lands in the file.
func (c *Config) managedValues() []keyValue {
	return []keyValue{
		{"source", tomlString(c.Source)},
		{"extra_sources", tomlStringList(c.ExtraSources)},
		{"direct_to_nas", strconv.FormatBool(c.DirectToNAS)},
		{"ssd_photos", tomlString(c.SSDPhotos)},
		{"ssd_videos", tomlString(c.SSDVideos)},
		{"nas_photos", tomlString(c.NASPhotos)},
		{"nas_videos", tomlString(c.NASVideos)},
		{"file_extensions", tomlStringList(c.FileExtensions)},
		{"video_extensions", tomlStringList(c.VideoExtensions)},
		{"ssd_workers", strconv.Itoa(c.SSDWorkerCount())},
		{"nas_workers", strconv.Itoa(c.NASWorkerCount())},
		{"nas_write_timeout_seconds", strconv.Itoa(int(c.NASWriteTimeout().Seconds()))},
		{"nas_sync_order", tomlString(c.SyncOrder())},
		{"list_preview", tomlString(c.ListPreview())},
	}
}

// applyValues rewrites each key's assignment line in content, appending the
// keys that are not present anywhere.
func applyValues(content string, values []keyValue) string {
	lines := strings.Split(content, "\n")
	var appended []keyValue

	// Rewriting a span shortens the slice, so the replacements are collected
	// first and applied from the bottom up — otherwise every index found after
	// the first multi-line array would point at the wrong line.
	type replacement struct {
		start, end int
		text       string
	}
	var edits []replacement

	for _, kv := range values {
		start, end := findAssignment(lines, kv.key)
		if start < 0 {
			appended = append(appended, kv)
			continue
		}
		edits = append(edits, replacement{start, end, kv.key + " = " + kv.value + trailingComment(lines[start])})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, e := range edits {
		lines = append(lines[:e.start], append([]string{e.text}, lines[e.end+1:]...)...)
	}

	out := strings.Join(lines, "\n")
	if len(appended) == 0 {
		return out
	}

	var sb strings.Builder
	sb.WriteString(out)
	if !strings.HasSuffix(out, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\n# Added by camera-backup\n")
	for _, kv := range appended {
		sb.WriteString(kv.key + " = " + kv.value + "\n")
	}
	return sb.String()
}

// findAssignment returns the line range [start, end] holding key's assignment:
// a live one if there is one, otherwise the first commented-out assignment.
// Returns (-1, -1) when the key appears nowhere.
//
// Exactly one assignment is ever rewritten. A valid TOML file cannot assign the
// same key twice, so any further match is prose — a comment that happens to
// read like an assignment — and turning that into config would be destructive.
// Live assignments win over commented ones so a template's `#key = default`
// hint is left alone once the key is actually set further down.
//
// An array value may run over several lines, which is the natural way to write
// one comment per file extension. end is then the line closing the bracket, and
// the whole span is replaced by a single-line assignment: the values survive,
// comments *between* the elements do not. Missing that continuation is what
// used to leave the array's tail behind as stray text and produce a config.toml
// the program could no longer load.
func findAssignment(lines []string, key string) (start, end int) {
	live := regexp.MustCompile(`^[\t ]*` + regexp.QuoteMeta(key) + `[\t ]*=`)
	commented := regexp.MustCompile(`^[\t ]*#[\t ]*` + regexp.QuoteMeta(key) + `[\t ]*=`)
	fallback := -1
	for i, line := range lines {
		if live.MatchString(line) {
			return i, assignmentEnd(lines, i)
		}
		if fallback < 0 && commented.MatchString(line) {
			fallback = i
		}
	}
	if fallback < 0 {
		return -1, -1
	}
	// A commented-out assignment is a single line by construction: its
	// continuation lines, if it ever had any, are comments of their own and
	// not part of any assignment.
	return fallback, fallback
}

// assignmentEnd returns the last line of the assignment starting at start,
// following an array value across lines until its brackets balance. An
// unbalanced value (a truncated file) ends the span at the last line rather
// than running past the end.
func assignmentEnd(lines []string, start int) int {
	_, value, found := strings.Cut(lines[start], "=")
	if !found {
		return start
	}
	depth := bracketDepth(value, 0)
	for i := start; ; i++ {
		if depth <= 0 {
			return i
		}
		if i+1 >= len(lines) {
			return i
		}
		depth = bracketDepth(lines[i+1], depth)
	}
}

// bracketDepth advances the bracket nesting depth across one line, ignoring
// brackets inside strings and comments so a path like "/mnt/[archive]" or a
// remark like "# see [tool.x]" cannot open a span that never closes.
func bracketDepth(line string, depth int) int {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '#':
			return depth
		case '\'':
			// TOML literal strings have no escapes.
			if j := strings.IndexByte(line[i+1:], '\''); j >= 0 {
				i += j + 1
				continue
			}
			return depth
		case '"':
			for i++; i < len(line); i++ {
				if line[i] == '\\' {
					i++
					continue
				}
				if line[i] == '"' {
					break
				}
			}
		case '[':
			depth++
		case ']':
			depth--
		}
	}
	return depth
}

// trailingComment returns the inline comment at the end of a TOML assignment
// (including one space of separation), or "" when there is none. Only a '#'
// outside a quoted string starts a comment.
func trailingComment(line string) string {
	// A commented-out assignment ("#ssd_workers = 3 # default 3") carries its
	// own leading '#'; drop it so the scan below finds the *trailing* comment
	// rather than stopping at the comment marker.
	body := strings.TrimPrefix(strings.TrimLeft(line, " \t"), "#")

	inString := false
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\\':
			if inString {
				i++ // skip the escaped character
			}
		case '"':
			inString = !inString
		case '#':
			if inString {
				continue
			}
			return " " + strings.TrimRight(body[i:], " \t")
		}
	}
	return ""
}

func tomlString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func tomlStringList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = tomlString(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
