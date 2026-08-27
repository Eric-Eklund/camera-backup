// White-box tests for the parts of Save that decide *which text* to replace —
// same package so the span finder and the pre-write guard can be exercised
// directly, without having to construct a whole broken file for each case.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBracketDepth(t *testing.T) {
	tests := []struct {
		name string
		line string
		in   int
		want int
	}{
		{"single-line array closes", `x = [".NEF", ".JPG"]`, 0, 0},
		{"array opens and stays open", `x = [`, 0, 1},
		{"continuation line", `  ".NEF",`, 1, 1},
		{"closing line", `]`, 1, 0},
		{"bracket inside a basic string", `x = "/mnt/[archive]/photos"`, 0, 0},
		{"unbalanced bracket inside a string", `x = "/mnt/[archive"`, 0, 0},
		{"bracket inside a literal string", `x = '/mnt/[archive]'`, 0, 0},
		{"bracket in a comment", `x = 1 # see [tool.thing]`, 0, 0},
		{"comment after an opening bracket", `x = [ # media`, 0, 1},
		{"escaped quote does not end the string", `x = "a\"[b"`, 0, 0},
		{"unterminated basic string", `x = "oops`, 0, 0},
		{"unterminated literal string", `x = 'oops`, 0, 0},
		{"nested arrays", `x = [[1], [2]]`, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bracketDepth(tc.line, tc.in); got != tc.want {
				t.Errorf("bracketDepth(%q, %d) = %d, want %d", tc.line, tc.in, got, tc.want)
			}
		})
	}
}

func TestFindAssignment(t *testing.T) {
	tests := []struct {
		name             string
		content          string
		key              string
		wantStart, wantE int
	}{
		{
			name:      "single line",
			content:   "source = \"/card\"\nssd_photos = \"/p\"\n",
			key:       "ssd_photos",
			wantStart: 1, wantE: 1,
		},
		{
			name:      "multi-line array spans to its closing bracket",
			content:   "file_extensions = [\n  \".NEF\",\n  \".JPG\",\n]\nsource = \"/card\"\n",
			key:       "file_extensions",
			wantStart: 0, wantE: 3,
		},
		{
			name:      "commented-out key is a single line",
			content:   "# nas_workers = 1\nsource = \"/card\"\n",
			key:       "nas_workers",
			wantStart: 0, wantE: 0,
		},
		{
			name:      "a live assignment beats a commented hint above it",
			content:   "#ssd_workers = 3\nfoo = 1\nssd_workers = 8\n",
			key:       "ssd_workers",
			wantStart: 2, wantE: 2,
		},
		{
			name:      "prose that merely looks like an assignment is not matched",
			content:   "source = \"/card\"\n# to change this, write source = \"/other\"\n",
			key:       "source",
			wantStart: 0, wantE: 0,
		},
		{
			name:      "a bracket inside the value does not open a span",
			content:   "source = \"/mnt/[card]\"\nssd_photos = \"/p\"\n",
			key:       "source",
			wantStart: 0, wantE: 0,
		},
		{
			name:      "an unterminated array stops at the end of the file",
			content:   "file_extensions = [\n  \".NEF\",\n",
			key:       "file_extensions",
			wantStart: 0, wantE: 2,
		},
		{
			name:      "absent key",
			content:   "source = \"/card\"\n",
			key:       "nas_photos",
			wantStart: -1, wantE: -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end := findAssignment(strings.Split(tc.content, "\n"), tc.key)
			if start != tc.wantStart || end != tc.wantE {
				t.Errorf("findAssignment = (%d, %d), want (%d, %d)", start, end, tc.wantStart, tc.wantE)
			}
		})
	}
}

// The guard is the backstop for every rewrite bug this file does not yet know
// about: whatever goes wrong, the user's config.toml must still load.
func TestVerifyRewrite_RejectsUnparsableOutput(t *testing.T) {
	want := []keyValue{{"source", `"/card"`}}
	err := verifyRewrite("source = \"/card\"\n  \".NEF\",\n]\n", want)
	if err == nil {
		t.Fatal("verifyRewrite accepted invalid TOML")
	}
	if !strings.Contains(err.Error(), "invalid TOML") {
		t.Errorf("error = %v, want it to say the output is not valid TOML", err)
	}
}

// A rewrite that parses but landed on the wrong line is just as bad as one
// that does not parse — the config loads, and quietly says something else.
func TestVerifyRewrite_RejectsAValueThatDidNotTakeEffect(t *testing.T) {
	cfg := &Config{
		Source: "/old-card", SSDPhotos: "/p", SSDVideos: "/v",
		FileExtensions: []string{".NEF"}, VideoExtensions: []string{".MOV"},
	}
	rendered := applyValues("", cfg.managedValues())

	// What Save meant to write: the same file with a new source. The rendered
	// text still carries the old one, which is what a misplaced rewrite leaves
	// behind.
	want := cfg.managedValues()
	want[0] = keyValue{"source", `"/new-card"`}

	err := verifyRewrite(rendered, want)
	if err == nil {
		t.Fatal("verifyRewrite accepted a file where the value never took effect")
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("error = %v, want it to name the key that did not take", err)
	}
}

func TestVerifyRewrite_AcceptsAFaithfulRewrite(t *testing.T) {
	cfg := &Config{
		Source: "/card", SSDPhotos: "/p", SSDVideos: "/v",
		FileExtensions: []string{".NEF"}, VideoExtensions: []string{".MOV"},
	}
	rendered := applyValues("", cfg.managedValues())
	if err := verifyRewrite(rendered, cfg.managedValues()); err != nil {
		t.Fatalf("verifyRewrite rejected its own output: %v", err)
	}
}

// ── End-to-end, through Save ────────────────────────────────────────────────

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The bug this file exists for: a perfectly valid config with one array
// written over several lines came back from Save as something the program
// could no longer load, so the tool would not start at all afterwards.
func TestSave_RewritesAMultiLineArrayWithoutBreakingTheFile(t *testing.T) {
	path := writeConfig(t, `source     = "/card"
ssd_photos = "/ssd/p"
ssd_videos = "/ssd/v"

# One extension per line, so the list is easy to edit.
file_extensions = [
  ".NEF",
  ".JPG",
  ".MOV",
]

video_extensions = [".MOV"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the fixture does not even load: %v", err)
	}
	cfg.Source = "/other-card"

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := Load(path)
	if err != nil {
		raw, _ := os.ReadFile(path)
		t.Fatalf("config no longer loads after Save: %v\n---\n%s", err, raw)
	}
	if reloaded.Source != "/other-card" {
		t.Errorf("source = %q, want the edit to have taken", reloaded.Source)
	}
	if got := strings.Join(reloaded.FileExtensions, ","); got != ".NEF,.JPG,.MOV" {
		t.Errorf("file_extensions = %v, want all three preserved", reloaded.FileExtensions)
	}

	// The tail of the array must be gone, not left behind as stray text.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\n  \".NEF\",") {
		t.Errorf("the array's continuation lines survived the rewrite:\n%s", raw)
	}
}

// Two multi-line arrays in one file: rewriting the first shortens the slice,
// so the second must still be found where it now is rather than where it was.
func TestSave_RewritesEveryMultiLineArrayInTheFile(t *testing.T) {
	path := writeConfig(t, `source     = "/card"
ssd_photos = "/ssd/p"
ssd_videos = "/ssd/v"

file_extensions = [
  ".NEF",
  ".JPG",
  ".MOV",
]

video_extensions = [
  ".MOV",
]

nas_workers = 1
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the fixture does not even load: %v", err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := Load(path)
	if err != nil {
		raw, _ := os.ReadFile(path)
		t.Fatalf("config no longer loads after Save: %v\n---\n%s", err, raw)
	}
	if strings.Join(reloaded.FileExtensions, ",") != ".NEF,.JPG,.MOV" {
		t.Errorf("file_extensions = %v", reloaded.FileExtensions)
	}
	if strings.Join(reloaded.VideoExtensions, ",") != ".MOV" {
		t.Errorf("video_extensions = %v", reloaded.VideoExtensions)
	}
	// The key that followed both arrays must not have been overwritten by the
	// shifting spans.
	if reloaded.NASWorkers != 1 {
		t.Errorf("nas_workers = %d, want 1 — a key after the arrays was clobbered", reloaded.NASWorkers)
	}
}

// Text that follows a multi-line array is not part of it and must survive.
func TestSave_KeepsWhatFollowsAMultiLineArray(t *testing.T) {
	path := writeConfig(t, `source     = "/card"
ssd_photos = "/ssd/p"
ssd_videos = "/ssd/v"
file_extensions = [
  ".NEF",
]
video_extensions = [".MOV"]

# A parting remark that must still be here afterwards.
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# A parting remark that must still be here afterwards.") {
		t.Errorf("the trailing comment was lost:\n%s", raw)
	}
}

// Saving twice in a row must be a no-op the second time: the first save
// collapses the arrays, and the second must find and rewrite the collapsed
// form rather than doing something new to it.
func TestSave_IsIdempotent(t *testing.T) {
	path := writeConfig(t, `source     = "/card"
ssd_photos = "/ssd/p"
ssd_videos = "/ssd/v"
file_extensions = [
  ".NEF",
  ".JPG",
]
video_extensions = [".MOV"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	cfg2, err := Load(path)
	if err != nil {
		t.Fatalf("Load after the first Save: %v", err)
	}
	if err := cfg2.Save(path); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("a second save changed the file again:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// Every managed key survives a save-and-reload with the value that was set,
// whatever shape it had in the file before.
func TestSave_RoundTripsEveryManagedKey(t *testing.T) {
	path := writeConfig(t, `source = "/card"
ssd_photos = "/ssd/p"
ssd_videos = "/ssd/v"
file_extensions = [
  ".NEF",
]
video_extensions = [".MOV"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Source = "/run/media/eric/CARD"
	cfg.ExtraSources = []string{"/run/media/eric/EXT"}
	cfg.NASPhotos = "/nas/p"
	cfg.NASVideos = "/nas/v"
	cfg.SSDWorkers = 5
	cfg.NASWorkers = 2
	cfg.NASWriteTimeoutSeconds = 90
	cfg.NASSyncOrder = OrderSizeAsc
	cfg.ListPreviewMode = PreviewBlocks
	cfg.FileExtensions = []string{".NEF", ".HIF"}
	cfg.VideoExtensions = []string{".MOV", ".MP4"}

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		raw, _ := os.ReadFile(path)
		t.Fatalf("Load after Save: %v\n---\n%s", err, raw)
	}

	want := cfg.managedValues()
	for i, kv := range got.managedValues() {
		if kv != want[i] {
			t.Errorf("%s = %s, want %s", kv.key, kv.value, want[i].value)
		}
	}
}

// A refused save leaves the original byte-identical. Losing the edit is
// recoverable; losing the config is not.
func TestSave_LeavesTheOriginalUntouchedWhenItRefuses(t *testing.T) {
	original := "source = \"/card\"\nssd_photos = \"/p\"\nssd_videos = \"/v\"\nfile_extensions = [\".NEF\"]\n"
	path := writeConfig(t, original)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SSDPhotos = "" // a lone ssd key — Validate refuses it

	if err := cfg.Save(path); err == nil {
		t.Fatal("Save accepted an invalid config")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Errorf("the file changed despite the refusal:\n%s", raw)
	}
}
