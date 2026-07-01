# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build (Windows target)
GOOS=windows GOARCH=amd64 go build -o camera-backup.exe ./cmd/camera-backup

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

### Copy phase details

Phase 1 (Camera → SSD) transforms paths: `DCIM/DSC_0001.NEF` → `photos/2026/2026-03/2026-03-25/DSC_0001.NEF`
(year → month → day hierarchy). Phase 2 (SSD → NAS) preserves relative paths directly — no transformation.

This split lets the user disconnect and power off the camera between phases. Phase 2 is optional and skipped gracefully if NAS is unavailable. In the TUI, Phase 2 tasks are always rescanned from the SSD after Phase 1 completes — never built from camera paths.

Comparison uses filename + size (not hash) for speed. Collision: same name but different size is treated as a new file and saved with a `_N` suffix — the source is never modified. On re-runs, `MissingFromDest` also probes the `_N` variants by size so collision files are not copied again.

### Key invariants

- Source files are never modified or deleted
- Destination files are created with `O_EXCL` — never overwritten
- Destination modtime is set to source modtime
- All extension and path comparisons are lowercased
- Free disk space is validated before any copy phase starts (CLI `checkSpace` in main.go, TUI `checkSpace` in ops.go)

### TUI specifics

- All background work runs as `tea.Cmd` goroutines; progress flows via a channel drained by `drainProgressCmd` → `p.Send()` (thread-safe)
- `statusReadyMsg`/`deviceChangedMsg` are ignored outside the loading/main screens so operations are never interrupted visually
- Selection (`space`/`a`) filters what `y` copies; empty selection = copy everything
- Worker counts come from `ssd_workers`/`nas_workers` in config.toml (defaults 3/1)
- Kitty graphics used for full-screen preview when `KittySupported()` (Ghostty/Kitty); block-art fallback otherwise; RAW previews need exiftool in PATH

### Platform-specific files

`internal/ui/freespace_windows.go` and `freespace_unix.go` implement `FreeSpace()` for each platform.
