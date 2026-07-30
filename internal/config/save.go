package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

	updated := applyValues(original, c.managedValues())

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
	}
}

// applyValues rewrites each key's assignment line in content, appending the
// keys that are not present anywhere.
func applyValues(content string, values []keyValue) string {
	lines := strings.Split(content, "\n")
	var appended []keyValue

	for _, kv := range values {
		i := findAssignment(lines, kv.key)
		if i < 0 {
			appended = append(appended, kv)
			continue
		}
		lines[i] = kv.key + " = " + kv.value + trailingComment(lines[i])
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
	sb.WriteString("\n# Added by the settings screen\n")
	for _, kv := range appended {
		sb.WriteString(kv.key + " = " + kv.value + "\n")
	}
	return sb.String()
}

// findAssignment returns the index of the line to rewrite for key: a live
// assignment if there is one, otherwise the first commented-out assignment.
// Returns -1 when the key appears nowhere.
//
// Exactly one line is ever rewritten. A valid TOML file cannot assign the same
// key twice, so any further match is prose — a comment that happens to read
// like an assignment — and turning that into config would be destructive. Live
// assignments win over commented ones so a template's `#key = default` hint is
// left alone once the key is actually set further down.
func findAssignment(lines []string, key string) int {
	live := regexp.MustCompile(`^[\t ]*` + regexp.QuoteMeta(key) + `[\t ]*=`)
	commented := regexp.MustCompile(`^[\t ]*#[\t ]*` + regexp.QuoteMeta(key) + `[\t ]*=`)
	fallback := -1
	for i, line := range lines {
		if live.MatchString(line) {
			return i
		}
		if fallback < 0 && commented.MatchString(line) {
			fallback = i
		}
	}
	return fallback
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
