# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Linux is the only target platform.

```bash
# Build
go build -o camera-backup ./cmd/camera-backup

# Run all tests
go test ./...

# Run tests for a specific package
go test ./internal/copyop -v

# Generate synthetic test data, then run against it
go run testdata/make_testdata.go
go run ./cmd/camera-backup --config testdata/config.toml status
go run ./cmd/camera-backup --config testdata/config.toml copy
go run ./cmd/camera-backup --config testdata/config.toml verify -v
go run ./cmd/camera-backup --config testdata/config.toml tui
```

## Architecture

Three-stage incremental backup pipeline: **Camera → SSD → NAS**

Five subcommands (Cobra CLI):
- `status` — scans all three destinations and shows missing file counts + free space
- `copy` — Camera→SSD (CopyAndVerify) then SSD→NAS (fast Copy)
- `sync` — SSD→NAS only, no camera required; `--videos-only/-v` flag; videos always first
- `verify` — deep SHA256 check; uses camera as authority, falls back to SSD if camera absent
- `tui` — interactive bubbletea TUI wrapping all of the above with parallel copy workers

**Package responsibilities:**

| Package | Role |
|---|---|
| `cmd/camera-backup` | CLI entry point, `runCopy()` + `runSync()` orchestration, logging init |
| `internal/config` | TOML loading, extension matching, `Category()` (photos/videos), worker counts |
| `internal/scan` | Recursive file walk, `MissingFromDest()` / `MissingByRelPath()` comparison |
| `internal/copyop` | `CopyAndVerify` (Sync+SHA256, Camera→SSD), `Copy` (fast, SSD→NAS), `RunBatch(verify bool)`, `RunBatchParallel()` (TUI worker pool with `FileProgress` events) |
| `internal/checksum` | SHA256 with optional progress writer |
| `internal/status` | Status command logic; `Compute()` returns `StatusResult` for the TUI |
| `internal/verify` | Verify command logic; `RunWithCallback()` streams per-file results to the TUI |
| `internal/preview` | Thumbnails (JPEG direct, NEF via exiftool), ANSI block art, Kitty Graphics Protocol |
| `internal/tui` | bubbletea Model/Update/View, ops launchers, fsnotify device watcher |
| `internal/ui` | Terminal colors, progress bar, `Prompt()`, `AskYesNo()`, `FreeSpace()` |

### Destination roots

Each device has **two destination roots** — one for photos, one for videos
(`ssd_photos`/`ssd_videos`/`nas_photos`/`nas_videos` in config.toml). Pointing
both keys at the same path merges the categories into one tree. A file's
category is **always decided by extension** (`video_extensions`; everything
else is photos) — never by which directory it happens to sit in — so a merged
SSD can be split onto separate NAS roots and vice versa. `copyop.Task` carries
its own `DstRoot`. If one category's root is unavailable, the other category is
still copied and skipped files are reported (CLI warning / TUI `⚠ NAS ✔P✘V`
badge + skip counts). A root "is available" when it or its parent directory
exists (`config.RootAvailable`) — the root itself is created on first copy.

### Copy phase details

Phase 1 (Camera → SSD) transforms paths: `DCIM/DSC_0001.NEF` → `<ssd_photos>/2026/2026-03/2026-03-25/DSC_0001.NEF`
(year → month → day hierarchy under the category root; `scan.FileInfo.DestRelPath()` is date-only).
Phase 2 (SSD → NAS) preserves relative paths within each category pair — no transformation.

This split lets the user disconnect and power off the camera between phases. Phase 2 is optional and skipped gracefully if NAS is unavailable. In the TUI, Phase 2 tasks are always rescanned from the SSD after Phase 1 completes — never built from camera paths.

Comparison uses filename + size (not hash) for speed. Collision: same name but different size is treated as a new file and saved with a `_N` suffix — the source is never modified. On re-runs, `MissingFromDest` also probes the `_N` variants by size so collision files are not copied again.

### Key invariants

- Source files are never modified or deleted
- Destination files are created with `O_EXCL` — never overwritten
- Destination modtime is set to source modtime
- All extension and path comparisons are lowercased
- Free disk space is validated before any copy phase starts (`copyop.CheckSpace`, shared by CLI and TUI; groups roots by filesystem so a shared disk isn't double-counted)

### TUI specifics

- All background work runs as `tea.Cmd` goroutines; progress flows via a channel drained by `drainProgressCmd` → `p.Send()` (thread-safe)
- The batch-done message is emitted by `drainProgressCmd` **after** all progress events have been forwarded — never emit it directly from `runBatchCmd`, or completion counts race against in-flight events
- `q`/`esc` on the progress screen cancels gracefully via `context`: files being copied finish (destination never holds partials), queued tasks don't start, cancelled tasks are not counted as failures; `ctrl+c` is a hard quit
- The progress screen re-renders twice a second (`progressTickCmd`) so per-file speeds stay live; the overall line shows total throughput and ETA
- `statusReadyMsg`/`deviceChangedMsg` are ignored outside the loading/main screens so operations are never interrupted visually
- Selection (`space`/`a`) filters what `y` copies; empty selection = copy everything. `y` also works from the grid view
- Grid view scrolls: `gridOffset` + `gridScrollToCursor()` keep the cursor row visible; thumbnails load incrementally for the visible window plus one lookahead row (no fixed cap)
- `?` opens the help screen (from main and grid); the Files panel title shows the visible row range (`Files 28–53/68`) when the tree overflows
- Worker counts come from `ssd_workers`/`nas_workers` in config.toml (defaults 3/1)
- Kitty graphics used for full-screen preview when `KittySupported()` (Ghostty/Kitty); block-art fallback otherwise; RAW previews need exiftool in PATH

### Verifying TUI changes

Run the TUI headless in tmux against generated testdata and inspect the actual rendering:

```bash
go run testdata/make_testdata.go
go build -o /tmp/cb ./cmd/camera-backup
tmux new-session -d -s tui -x 120 -y 40 '/tmp/cb --config testdata/config.toml tui'
tmux send-keys -t tui j        # navigate/interact (one key per call, small sleep between)
tmux capture-pane -t tui -p    # inspect the rendered screen (-e keeps colors/focus)
tmux kill-server
```

- Also test at 80×24 — the Info panel hides below ~70 columns and rows must not wrap
- To exercise progress/cancel/ETA, drop large files into the camera testdata first:
  `dd if=/dev/urandom of=testdata/camera/DCIM/100NIKON/BIG_0001.MOV bs=1M count=300`
- Reset testdata between runs: `rm -rf testdata/camera testdata/ssd testdata/nas`

### Platform-specific files

Linux is the only supported target. `internal/ui/freespace_unix.go` implements `FreeSpace()`; `freespace_windows.go` remains in the tree (build-tagged, never compiled on Linux) in case Windows support is ever revived.
