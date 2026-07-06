package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Source          string   `toml:"source"`
	SSDPhotos       string   `toml:"ssd_photos"`
	SSDVideos       string   `toml:"ssd_videos"`
	NASPhotos       string   `toml:"nas_photos"`
	NASVideos       string   `toml:"nas_videos"`
	FileExtensions  []string `toml:"file_extensions"`
	VideoExtensions []string `toml:"video_extensions"`
	SSDWorkers      int      `toml:"ssd_workers"`
	NASWorkers      int      `toml:"nas_workers"`

	// Legacy single-root keys — rejected with a migration hint in Load.
	LegacySSD string `toml:"ssd"`
	LegacyNAS string `toml:"nas"`
}

// SSDRoot returns the SSD destination root for a category ("photos"/"videos").
func (c *Config) SSDRoot(category string) string {
	if category == "videos" {
		return c.SSDVideos
	}
	return c.SSDPhotos
}

// NASRoot returns the NAS destination root for a category ("photos"/"videos").
// Empty when the NAS is not configured.
func (c *Config) NASRoot(category string) string {
	if category == "videos" {
		return c.NASVideos
	}
	return c.NASPhotos
}

// NASConfigured reports whether any NAS destination is set.
func (c *Config) NASConfigured() bool {
	return c.NASPhotos != "" || c.NASVideos != ""
}

// SSDMerged reports whether photos and videos share one SSD root.
func (c *Config) SSDMerged() bool { return c.SSDPhotos == c.SSDVideos }

// NASMerged reports whether photos and videos share one NAS root.
func (c *Config) NASMerged() bool { return c.NASPhotos == c.NASVideos }

func (c *Config) SSDWorkerCount() int {
	if c.SSDWorkers > 0 {
		return c.SSDWorkers
	}
	return 3
}

func (c *Config) NASWorkerCount() int {
	if c.NASWorkers > 0 {
		return c.NASWorkers
	}
	return 1
}

// NormalisedExtensions returns all file_extensions lowercased.
func (c *Config) NormalisedExtensions() []string {
	return normalise(c.FileExtensions)
}

// NormalisedVideoExtensions returns video_extensions lowercased.
func (c *Config) NormalisedVideoExtensions() []string {
	return normalise(c.VideoExtensions)
}

// Category returns "videos" if the filename matches a video extension,
// otherwise "photos".
func (c *Config) Category(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	for _, e := range c.NormalisedVideoExtensions() {
		if e == ext {
			return "videos"
		}
	}
	return "photos"
}

// RootAvailable reports whether a destination root can be used: either the
// directory itself exists, or its parent does — in which case the root is
// created on first copy (mirroring how photos/ and videos/ subdirectories
// used to be created under a mounted device). An unmounted device fails both
// checks. Empty paths are never available.
func RootAvailable(path string) bool {
	if path == "" {
		return false
	}
	return isDir(path) || isDir(filepath.Dir(path))
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// DefaultConfigPath returns the path to config.toml next to the running binary.
func DefaultConfigPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine executable path: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "config.toml"), nil
}

func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("loading config %q: %w", path, err)
	}
	if cfg.LegacySSD != "" || cfg.LegacyNAS != "" {
		return nil, fmt.Errorf(`config: the single-root keys "ssd"/"nas" are no longer supported.
Point each category at its own directory instead:

  ssd_photos = %q
  ssd_videos = %q
  nas_photos = "..."
  nas_videos = "..."

Use the same path for photos and videos to merge them into one tree.
Note: destinations no longer get an automatic photos/ or videos/ subdirectory —
point the keys at your existing <root>/photos and <root>/videos directories`,
			cfg.LegacySSD+"/photos", cfg.LegacySSD+"/videos")
	}
	if cfg.Source == "" {
		return nil, fmt.Errorf("config: source path is required")
	}
	if cfg.SSDPhotos == "" || cfg.SSDVideos == "" {
		return nil, fmt.Errorf("config: ssd_photos and ssd_videos are both required (use the same path to merge)")
	}
	// NAS is optional, but a lone key is almost certainly a mistake.
	if (cfg.NASPhotos == "") != (cfg.NASVideos == "") {
		return nil, fmt.Errorf("config: set both nas_photos and nas_videos (use the same path to merge), or neither")
	}
	return &cfg, nil
}

func normalise(exts []string) []string {
	out := make([]string, len(exts))
	for i, e := range exts {
		out[i] = strings.ToLower(e)
	}
	return out
}
