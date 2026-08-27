package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Source string `toml:"source"`
	// ExtraSources are additional source devices (a second card reader, an
	// external SSD) tried in order when Source is not mounted. See ActiveSource.
	ExtraSources    []string `toml:"extra_sources"`
	SSDPhotos       string   `toml:"ssd_photos"`
	SSDVideos       string   `toml:"ssd_videos"`
	NASPhotos       string   `toml:"nas_photos"`
	NASVideos       string   `toml:"nas_videos"`
	FileExtensions  []string `toml:"file_extensions"`
	VideoExtensions []string `toml:"video_extensions"`
	SSDWorkers      int      `toml:"ssd_workers"`
	NASWorkers      int      `toml:"nas_workers"`

	// DirectToNAS dumps the source device straight to the NAS, bypassing the
	// local SSD entirely. The NAS is then the only copy, so these transfers
	// are always SHA256-verified.
	DirectToNAS bool `toml:"direct_to_nas"`

	NASWriteTimeoutSeconds int    `toml:"nas_write_timeout_seconds"`
	NASSyncOrder           string `toml:"nas_sync_order"`

	// ListPreviewMode decides how the TUI's Info panel draws the focused
	// photograph. See the Preview* constants.
	ListPreviewMode string `toml:"list_preview"`

	// Legacy single-root keys — rejected with a migration hint in Load.
	LegacySSD string `toml:"ssd"`
	LegacyNAS string `toml:"nas"`

	// sourceOverride is a source device picked at runtime (the TUI's device
	// picker). It is not a config key: swapping cards mid-session should not
	// rewrite config.toml, so it lives in front of source/extra_sources for
	// this run only. See SetSourceOverride.
	sourceOverride string
}

// SetSourceOverride makes path the first source candidate for this run,
// without touching config.toml. Pass "" to drop back to the configured
// devices.
func (c *Config) SetSourceOverride(path string) { c.sourceOverride = path }

// SourceOverride returns the runtime source device, "" when none is set.
func (c *Config) SourceOverride() string { return c.sourceOverride }

// SourceCandidates returns every source device in priority order: a runtime
// override first (see SetSourceOverride), then source, then extra_sources.
func (c *Config) SourceCandidates() []string {
	out := make([]string, 0, 2+len(c.ExtraSources))
	if c.sourceOverride != "" {
		out = append(out, c.sourceOverride)
	}
	if c.Source != "" && c.Source != c.sourceOverride {
		out = append(out, c.Source)
	}
	for _, p := range c.ExtraSources {
		if p != "" && p != c.sourceOverride {
			out = append(out, p)
		}
	}
	return out
}

// ActiveSource returns the first source device that is currently mounted — the
// runtime override, then source, then each entry of extra_sources in order.
// This is what lets one config serve a camera card reader and an external SSD:
// plug either one in and it becomes the source. A device picked in the TUI
// wins while it stays mounted, and unplugging it falls back to the configured
// list rather than leaving the source pointing at an empty slot. When nothing
// is mounted it returns source so messages still name a real configured path.
//
// Unlike a destination root, a source must exist as a directory: an empty
// mount point would otherwise look like an empty card.
func (c *Config) ActiveSource() string {
	for _, p := range c.SourceCandidates() {
		if isDir(p) {
			return p
		}
	}
	return c.Source
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

// SSDConfigured reports whether a local SSD destination is set. It is only
// optional in direct_to_nas mode, where the SSD is bypassed anyway.
func (c *Config) SSDConfigured() bool {
	return c.SSDPhotos != "" || c.SSDVideos != ""
}

// SSDInUse reports whether copies should route through the local SSD at all.
// direct_to_nas takes the SSD out of the picture even when its roots are still
// configured — the `sync` command can still push an existing SSD tree to the
// NAS on demand.
func (c *Config) SSDInUse() bool {
	return c.SSDConfigured() && !c.DirectToNAS
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

// Transfer orders for SSD→NAS copies (nas_sync_order / sync --order).
const (
	OrderVideosFirst = "videos-first" // default: large videos go first
	OrderSizeAsc     = "size-asc"     // smallest files first — best on flaky links
)

// SyncOrder returns the configured SSD→NAS transfer order, used by the TUI
// and as the default for sync --order. Defaults to videos-first when unset.
func (c *Config) SyncOrder() string {
	if c.NASSyncOrder != "" {
		return c.NASSyncOrder
	}
	return OrderVideosFirst
}

// How the Info panel draws its preview (list_preview).
const (
	// PreviewAuto uses the Kitty Graphics Protocol where the terminal speaks
	// it (Kitty, Ghostty) and falls back to block art everywhere else. Block
	// art spends one pixel per terminal column, which in a panel that narrow
	// is barely a picture — where a real image can be drawn, it should be.
	PreviewAuto = "auto"
	// PreviewKitty asks for the graphics protocol even where detection says
	// no — inside tmux with allow-passthrough turned on, for instance.
	PreviewKitty = "kitty"
	// PreviewBlocks forces the ANSI half-block rendering.
	PreviewBlocks = "blocks"
	// PreviewOff leaves the panel to the file's details.
	PreviewOff = "off"
)

// ListPreview returns how the Info panel should draw previews, defaulting to
// auto.
func (c *Config) ListPreview() string {
	if c.ListPreviewMode != "" {
		return c.ListPreviewMode
	}
	return PreviewAuto
}

// NASWriteTimeout returns the per-file write timeout for SSD→NAS copies.
// A hard-mounted NFS/CIFS share blocks indefinitely instead of erroring when
// the connection drops, so writes are bounded rather than trusted to fail.
// Defaults to 60s when nas_write_timeout_seconds is unset.
func (c *Config) NASWriteTimeout() time.Duration {
	if c.NASWriteTimeoutSeconds > 0 {
		return time.Duration(c.NASWriteTimeoutSeconds) * time.Second
	}
	return 60 * time.Second
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

// FirstRunDefaults returns the configuration a fresh install starts from on
// the TUI's first-run settings screen: the extension lists from
// config-template.toml, with every path left for the user to fill in. It
// deliberately does not pass Validate — it exists to be edited, and the first
// save is what has to produce a valid config.
func FirstRunDefaults() *Config {
	return &Config{
		FileExtensions:  []string{".MOV", ".NEF", ".JPG", ".MP4", ".HEIC", ".HIF"},
		VideoExtensions: []string{".MOV", ".MP4"},
	}
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
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks the combination of keys for consistency. Shared by Load and
// by the TUI's settings screen, which validates edits before writing them back.
func (c *Config) Validate() error {
	if c.Source == "" {
		return fmt.Errorf("config: source path is required")
	}
	// The SSD is required unless it is bypassed by direct_to_nas — but a lone
	// key is a mistake either way.
	if (c.SSDPhotos == "") != (c.SSDVideos == "") {
		return fmt.Errorf("config: set both ssd_photos and ssd_videos (use the same path to merge), or neither")
	}
	if !c.DirectToNAS && !c.SSDConfigured() {
		return fmt.Errorf("config: ssd_photos and ssd_videos are both required (use the same path to merge), or set direct_to_nas = true to dump straight to the NAS")
	}
	// NAS is optional, but a lone key is almost certainly a mistake.
	if (c.NASPhotos == "") != (c.NASVideos == "") {
		return fmt.Errorf("config: set both nas_photos and nas_videos (use the same path to merge), or neither")
	}
	// direct_to_nas has nowhere to copy to without a NAS.
	if c.DirectToNAS && !c.NASConfigured() {
		return fmt.Errorf("config: direct_to_nas requires nas_photos and nas_videos (use the same path to merge)")
	}
	if c.NASSyncOrder != "" && c.NASSyncOrder != OrderVideosFirst && c.NASSyncOrder != OrderSizeAsc {
		return fmt.Errorf("config: nas_sync_order must be %q or %q", OrderVideosFirst, OrderSizeAsc)
	}
	switch c.ListPreviewMode {
	case "", PreviewAuto, PreviewKitty, PreviewBlocks, PreviewOff:
	default:
		return fmt.Errorf("config: list_preview must be %q, %q, %q or %q",
			PreviewAuto, PreviewKitty, PreviewBlocks, PreviewOff)
	}
	// An empty extension list is not "no filter" — it is a scan that sees
	// nothing. Every command would then agree the backup is complete: 0 files
	// found, 0 missing, "All 0 files verified OK" — while never looking at a
	// single photograph. The TUI's settings form rejects this at the field
	// level; this is the backstop for a hand-edited config.toml.
	if len(c.FileExtensions) == 0 {
		return fmt.Errorf("config: file_extensions must list at least one extension — an empty list would make every scan see nothing and report the backup as complete")
	}
	return nil
}

func normalise(exts []string) []string {
	out := make([]string, len(exts))
	for i, e := range exts {
		out[i] = strings.ToLower(e)
	}
	return out
}
