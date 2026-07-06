package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
