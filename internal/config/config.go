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
	SSD             string   `toml:"ssd"`
	NAS             string   `toml:"nas"`
	FileExtensions  []string `toml:"file_extensions"`
	VideoExtensions []string `toml:"video_extensions"`
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
	if cfg.Source == "" {
		return nil, fmt.Errorf("config: source path is required")
	}
	if cfg.SSD == "" {
		return nil, fmt.Errorf("config: ssd path is required")
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
