package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Eric-Eklund/camera-backup/internal/config"
)

func loadSaveReload(t *testing.T, path string, mutate func(*config.Config)) (before, after *config.Config, text string) {
	t.Helper()
	before, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mutate(before)
	if err := before.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err = config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return before, after, string(b)
}

// The settings screen must be able to flip direct_to_nas without disturbing
// the rest of a hand-maintained file.
func TestSave_UpdatesKeyInPlaceKeepingComments(t *testing.T) {
	path := writeTempConfig(t, `# My camera backup config
source     = "/cam"   # the card reader
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
nas_photos = "/nas/Photos"
nas_videos = "/nas/Videos"

# Dump straight to the NAS?
#direct_to_nas = false

file_extensions  = [".NEF", ".JPG"]
video_extensions = [".MOV"]
`)
	_, after, text := loadSaveReload(t, path, func(c *config.Config) {
		c.DirectToNAS = true
	})

	if !after.DirectToNAS {
		t.Error("direct_to_nas did not survive the round trip")
	}
	if !strings.Contains(text, "# My camera backup config") {
		t.Error("leading comment was lost")
	}
	if !strings.Contains(text, "# Dump straight to the NAS?") {
		t.Error("comment above the key was lost")
	}
	if !strings.Contains(text, "direct_to_nas = true") {
		t.Errorf("key was not activated:\n%s", text)
	}
	if strings.Contains(text, "#direct_to_nas") {
		t.Errorf("commented-out key was left behind:\n%s", text)
	}
	// Replaced in place, not appended a second time. (Keys the file never had —
	// ssd_workers and friends — are legitimately appended.)
	if n := strings.Count(text, "direct_to_nas ="); n != 1 {
		t.Errorf("direct_to_nas appears %d times, want 1:\n%s", n, text)
	}
}

func TestSave_PreservesTrailingComment(t *testing.T) {
	path := writeTempConfig(t, `source     = "/cam"   # the card reader
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
file_extensions = [".NEF"]
`)
	_, after, text := loadSaveReload(t, path, func(c *config.Config) {
		c.Source = "/run/media/eric/NIKON"
	})

	if after.Source != "/run/media/eric/NIKON" {
		t.Errorf("Source = %q", after.Source)
	}
	if !strings.Contains(text, `source = "/run/media/eric/NIKON" # the card reader`) {
		t.Errorf("trailing comment was lost:\n%s", text)
	}
}

// A commented-out key with a trailing hint keeps the hint when activated.
func TestSave_PreservesTrailingCommentOnCommentedKey(t *testing.T) {
	path := writeTempConfig(t, `source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
#ssd_workers = 3 # camera → SSD (default 3)
file_extensions = [".NEF"]
`)
	_, after, text := loadSaveReload(t, path, func(c *config.Config) {
		c.SSDWorkers = 6
	})

	if after.SSDWorkerCount() != 6 {
		t.Errorf("SSDWorkerCount() = %d, want 6", after.SSDWorkerCount())
	}
	if !strings.Contains(text, "ssd_workers = 6 # camera → SSD (default 3)") {
		t.Errorf("trailing comment was lost:\n%s", text)
	}
}

func TestSave_AppendsMissingKeys(t *testing.T) {
	path := writeTempConfig(t, `source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
file_extensions = [".NEF"]
`)
	_, after, text := loadSaveReload(t, path, func(c *config.Config) {
		c.ExtraSources = []string{"/media/ext"}
	})

	if len(after.ExtraSources) != 1 || after.ExtraSources[0] != "/media/ext" {
		t.Errorf("ExtraSources = %v", after.ExtraSources)
	}
	if !strings.Contains(text, `extra_sources = ["/media/ext"]`) {
		t.Errorf("appended key missing:\n%s", text)
	}
}

// Prose that merely looks like an assignment must not be turned into config.
func TestSave_IgnoresProseCommentWhenKeyIsSet(t *testing.T) {
	path := writeTempConfig(t, `# nas_sync_order = "videos-first" (default) or "size-asc"
source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
file_extensions = [".NEF"]
nas_sync_order = "videos-first"
`)
	_, after, text := loadSaveReload(t, path, func(c *config.Config) {
		c.NASSyncOrder = config.OrderSizeAsc
	})

	if after.SyncOrder() != config.OrderSizeAsc {
		t.Errorf("SyncOrder() = %q", after.SyncOrder())
	}
	if !strings.Contains(text, `# nas_sync_order = "videos-first" (default) or "size-asc"`) {
		t.Errorf("prose comment was rewritten:\n%s", text)
	}
	if strings.Count(text, "\nnas_sync_order = ") != 1 {
		t.Errorf("expected exactly one live assignment:\n%s", text)
	}
}

// Emptying the SSD roots is only valid together with direct_to_nas — Save must
// refuse an invalid combination rather than write a config that cannot load.
func TestSave_RejectsInvalidConfig(t *testing.T) {
	path := writeTempConfig(t, `source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
file_extensions = [".NEF"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	original, _ := os.ReadFile(path)

	cfg.SSDPhotos, cfg.SSDVideos = "", ""
	if err := cfg.Save(path); err == nil {
		t.Fatal("expected Save to reject a config with no SSD and no direct_to_nas")
	}

	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Error("file was modified despite the validation failure")
	}
}

func TestSave_QuotesAreEscaped(t *testing.T) {
	path := writeTempConfig(t, `source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
file_extensions = [".NEF"]
`)
	_, after, _ := loadSaveReload(t, path, func(c *config.Config) {
		c.Source = `/media/od"d\path`
	})
	if after.Source != `/media/od"d\path` {
		t.Errorf("Source = %q, want %q", after.Source, `/media/od"d\path`)
	}
}

// Saving a config that omits every optional key writes effective values, so the
// file ends up describing exactly what the tool is doing.
func TestSave_WritesEffectiveDefaults(t *testing.T) {
	path := writeTempConfig(t, `source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
file_extensions = [".NEF"]
`)
	_, after, text := loadSaveReload(t, path, func(c *config.Config) {})

	for _, want := range []string{
		"ssd_workers = 3",
		"nas_workers = 1",
		"nas_write_timeout_seconds = 60",
		`nas_sync_order = "videos-first"`,
		"direct_to_nas = false",
		"extra_sources = []",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in saved config:\n%s", want, text)
		}
	}
	if after.SSDWorkerCount() != 3 || after.NASWorkerCount() != 1 {
		t.Errorf("worker counts changed: %d/%d", after.SSDWorkerCount(), after.NASWorkerCount())
	}
}

// Round-tripping the shipped template must not corrupt it.
func TestSave_TemplateRoundTrip(t *testing.T) {
	b, err := os.ReadFile("../../config-template.toml")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	path := writeTempConfig(t, string(b))

	before, after, text := loadSaveReload(t, path, func(c *config.Config) {
		c.DirectToNAS = true
	})

	if after.Source != before.Source {
		t.Errorf("Source changed: %q → %q", before.Source, after.Source)
	}
	if !after.DirectToNAS {
		t.Error("direct_to_nas was not set")
	}
	if len(after.FileExtensions) != len(before.FileExtensions) {
		t.Errorf("file_extensions changed: %v → %v", before.FileExtensions, after.FileExtensions)
	}
	// The template's explanatory prose must still be there.
	if !strings.Contains(text, "# camera-backup configuration template.") {
		t.Errorf("template header was lost:\n%s", text)
	}
	if strings.Contains(text, "Added by camera-backup") {
		t.Errorf("template should already contain every managed key:\n%s", text)
	}
}
