package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eric-Eklund/camera-backup/internal/config"
)

func TestCategory_Photos(t *testing.T) {
	cfg := &config.Config{VideoExtensions: []string{".MOV", ".MP4"}}
	for _, name := range []string{"DSC_0001.NEF", "DSC_0001.nef", "photo.JPG", "photo.jpg"} {
		if got := cfg.Category(name); got != "photos" {
			t.Errorf("Category(%q) = %q, want \"photos\"", name, got)
		}
	}
}

func TestCategory_Videos(t *testing.T) {
	cfg := &config.Config{VideoExtensions: []string{".MOV", ".MP4"}}
	for _, name := range []string{"VID_0001.MOV", "VID_0001.mov", "clip.MP4", "clip.mp4"} {
		if got := cfg.Category(name); got != "videos" {
			t.Errorf("Category(%q) = %q, want \"videos\"", name, got)
		}
	}
}

func TestNormalisedExtensions(t *testing.T) {
	cfg := &config.Config{FileExtensions: []string{".NEF", ".JPG", ".MOV"}}
	got := cfg.NormalisedExtensions()
	want := []string{".nef", ".jpg", ".mov"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoad_Valid(t *testing.T) {
	path := writeTempConfig(t, `
source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
nas_photos = "/nas/Photos"
nas_videos = "/nas/Videos"
file_extensions  = [".NEF", ".JPG"]
video_extensions = [".MOV"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Source != "/cam" {
		t.Errorf("Source = %q", cfg.Source)
	}
	if cfg.SSDRoot("photos") != "/ssd/Photos" {
		t.Errorf("SSDRoot(photos) = %q", cfg.SSDRoot("photos"))
	}
	if cfg.SSDRoot("videos") != "/ssd/Videos" {
		t.Errorf("SSDRoot(videos) = %q", cfg.SSDRoot("videos"))
	}
	if cfg.NASRoot("videos") != "/nas/Videos" {
		t.Errorf("NASRoot(videos) = %q", cfg.NASRoot("videos"))
	}
	if !cfg.NASConfigured() {
		t.Error("NASConfigured() = false, want true")
	}
	if cfg.SSDMerged() {
		t.Error("SSDMerged() = true for distinct roots")
	}
}

func TestLoad_MergedRoots(t *testing.T) {
	path := writeTempConfig(t, `
source     = "/cam"
ssd_photos = "/ssd/All"
ssd_videos = "/ssd/All"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.SSDMerged() {
		t.Error("SSDMerged() = false for identical roots")
	}
	if cfg.NASConfigured() {
		t.Error("NASConfigured() = true with no NAS keys")
	}
}

func TestNASWriteTimeout_Default(t *testing.T) {
	cfg := &config.Config{}
	if got := cfg.NASWriteTimeout(); got != 60*time.Second {
		t.Errorf("NASWriteTimeout() = %v, want 60s default", got)
	}
}

func TestNASWriteTimeout_FromConfig(t *testing.T) {
	path := writeTempConfig(t, `
source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
nas_write_timeout_seconds = 15
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.NASWriteTimeout(); got != 15*time.Second {
		t.Errorf("NASWriteTimeout() = %v, want 15s", got)
	}
}

func TestSyncOrder_Default(t *testing.T) {
	cfg := &config.Config{}
	if got := cfg.SyncOrder(); got != config.OrderVideosFirst {
		t.Errorf("SyncOrder() = %q, want %q", got, config.OrderVideosFirst)
	}
}

func TestSyncOrder_FromConfig(t *testing.T) {
	path := writeTempConfig(t, `
source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
nas_sync_order = "size-asc"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.SyncOrder(); got != config.OrderSizeAsc {
		t.Errorf("SyncOrder() = %q, want %q", got, config.OrderSizeAsc)
	}
}

func TestLoad_RejectsInvalidSyncOrder(t *testing.T) {
	path := writeTempConfig(t, `
source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
nas_sync_order = "biggest-first"
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for invalid nas_sync_order")
	}
}

func TestLoad_RejectsLegacyKeys(t *testing.T) {
	path := writeTempConfig(t, `
source = "/cam"
ssd    = "/ssd"
nas    = "/nas"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for legacy ssd/nas keys")
	}
	if !strings.Contains(err.Error(), "ssd_photos") {
		t.Errorf("error should include migration hint, got: %v", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := config.Load("/nonexistent/config.toml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_MissingSource(t *testing.T) {
	path := writeTempConfig(t, `
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error when source is empty")
	}
}

func TestLoad_MissingSSDRoot(t *testing.T) {
	path := writeTempConfig(t, `
source     = "/cam"
ssd_photos = "/ssd/Photos"
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error when ssd_videos is empty")
	}
}

func TestLoad_LoneNASKey(t *testing.T) {
	path := writeTempConfig(t, `
source     = "/cam"
ssd_photos = "/ssd/Photos"
ssd_videos = "/ssd/Videos"
nas_photos = "/nas/Photos"
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error when only one NAS key is set")
	}
}

// ── direct_to_nas ─────────────────────────────────────────────────────────────

func TestLoad_DirectToNASWithoutSSD(t *testing.T) {
	path := writeTempConfig(t, `
source        = "/cam"
nas_photos    = "/nas/Photos"
nas_videos    = "/nas/Videos"
direct_to_nas = true
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.DirectToNAS {
		t.Error("DirectToNAS = false, want true")
	}
	if cfg.SSDConfigured() {
		t.Error("SSDConfigured() = true with no SSD keys")
	}
	if cfg.SSDInUse() {
		t.Error("SSDInUse() = true in direct mode")
	}
}

// direct_to_nas takes the SSD out of the copy path even when its roots are
// still configured, so `sync` keeps working.
func TestLoad_DirectToNASKeepsSSDConfigured(t *testing.T) {
	path := writeTempConfig(t, `
source        = "/cam"
ssd_photos    = "/ssd/Photos"
ssd_videos    = "/ssd/Videos"
nas_photos    = "/nas/Photos"
nas_videos    = "/nas/Videos"
direct_to_nas = true
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.SSDConfigured() {
		t.Error("SSDConfigured() = false, want true")
	}
	if cfg.SSDInUse() {
		t.Error("SSDInUse() = true in direct mode")
	}
}

func TestLoad_DirectToNASRequiresNAS(t *testing.T) {
	path := writeTempConfig(t, `
source        = "/cam"
direct_to_nas = true
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for direct_to_nas without a NAS")
	}
	if !strings.Contains(err.Error(), "nas_photos") {
		t.Errorf("error should name the missing NAS keys, got: %v", err)
	}
}

func TestLoad_DirectToNASRejectsLoneSSDKey(t *testing.T) {
	path := writeTempConfig(t, `
source        = "/cam"
ssd_photos    = "/ssd/Photos"
nas_photos    = "/nas/Photos"
nas_videos    = "/nas/Videos"
direct_to_nas = true
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error when only one SSD key is set")
	}
}

func TestLoad_MissingSSDSuggestsDirectMode(t *testing.T) {
	path := writeTempConfig(t, `
source = "/cam"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error when no SSD roots are set")
	}
	if !strings.Contains(err.Error(), "direct_to_nas") {
		t.Errorf("error should mention direct_to_nas, got: %v", err)
	}
}

// ── source resolution ─────────────────────────────────────────────────────────

func TestActiveSource_PrefersSourceWhenMounted(t *testing.T) {
	card := t.TempDir()
	drive := t.TempDir()
	cfg := &config.Config{Source: card, ExtraSources: []string{drive}}
	if got := cfg.ActiveSource(); got != card {
		t.Errorf("ActiveSource() = %q, want %q", got, card)
	}
}

func TestActiveSource_FallsBackToExtraSource(t *testing.T) {
	drive := t.TempDir()
	cfg := &config.Config{
		Source:       filepath.Join(t.TempDir(), "not-mounted"),
		ExtraSources: []string{filepath.Join(t.TempDir(), "also-absent"), drive},
	}
	if got := cfg.ActiveSource(); got != drive {
		t.Errorf("ActiveSource() = %q, want %q", got, drive)
	}
}

// With nothing mounted the configured source is returned so messages can name
// a real path instead of an empty string.
func TestActiveSource_NothingMounted(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-mounted")
	cfg := &config.Config{Source: missing, ExtraSources: []string{filepath.Join(t.TempDir(), "gone")}}
	if got := cfg.ActiveSource(); got != missing {
		t.Errorf("ActiveSource() = %q, want %q", got, missing)
	}
}

func TestSourceCandidates_SkipsEmpty(t *testing.T) {
	cfg := &config.Config{Source: "/cam", ExtraSources: []string{"", "/drive"}}
	got := cfg.SourceCandidates()
	want := []string{"/cam", "/drive"}
	if len(got) != len(want) {
		t.Fatalf("SourceCandidates() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoad_ExtraSources(t *testing.T) {
	path := writeTempConfig(t, `
source        = "/cam"
extra_sources = ["/run/media/user/EXT-SSD"]
nas_photos    = "/nas/Photos"
nas_videos    = "/nas/Videos"
direct_to_nas = true
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.ExtraSources) != 1 || cfg.ExtraSources[0] != "/run/media/user/EXT-SSD" {
		t.Errorf("ExtraSources = %v", cfg.ExtraSources)
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestSourceOverride_WinsWhileMounted(t *testing.T) {
	card := t.TempDir()
	picked := t.TempDir()
	cfg := &config.Config{Source: card}
	cfg.SetSourceOverride(picked)

	if got := cfg.ActiveSource(); got != picked {
		t.Errorf("ActiveSource() = %q, want the picked device %q", got, picked)
	}
	if got := cfg.SourceOverride(); got != picked {
		t.Errorf("SourceOverride() = %q, want %q", got, picked)
	}
	if cands := cfg.SourceCandidates(); len(cands) != 2 || cands[0] != picked || cands[1] != card {
		t.Errorf("SourceCandidates() = %v, want [%q %q]", cands, picked, card)
	}
}

// Unplugging a picked card must not leave the source pointing at an empty
// slot — the configured devices are still there to fall back on.
func TestSourceOverride_FallsBackWhenUnmounted(t *testing.T) {
	card := t.TempDir()
	cfg := &config.Config{Source: card}
	cfg.SetSourceOverride(filepath.Join(t.TempDir(), "unplugged"))

	if got := cfg.ActiveSource(); got != card {
		t.Errorf("ActiveSource() = %q, want the configured card %q", got, card)
	}
}

func TestSourceOverride_NotDuplicatedInCandidates(t *testing.T) {
	cfg := &config.Config{Source: "/cam", ExtraSources: []string{"/drive"}}
	cfg.SetSourceOverride("/drive")

	cands := cfg.SourceCandidates()
	if len(cands) != 2 || cands[0] != "/drive" || cands[1] != "/cam" {
		t.Errorf("SourceCandidates() = %v, want [/drive /cam]", cands)
	}
}

func TestSourceOverride_ClearedRestoresConfigured(t *testing.T) {
	card := t.TempDir()
	picked := t.TempDir()
	cfg := &config.Config{Source: card}
	cfg.SetSourceOverride(picked)
	cfg.SetSourceOverride("")

	if got := cfg.ActiveSource(); got != card {
		t.Errorf("ActiveSource() = %q, want %q after clearing the override", got, card)
	}
}
