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
```

## Architecture

Three-stage incremental backup pipeline: **Camera → SSD → NAS**

Three subcommands (Cobra CLI):
- `status` — scans all three destinations and shows missing file counts + free space
- `copy` — two-phase copy with SHA256 verification after each file
- `verify` — deep integrity check (SHA256) across all destinations

**Package responsibilities:**

| Package | Role |
|---|---|
| `cmd/camera-backup` | CLI entry point, `runCopy()` orchestration, logging init |
| `internal/config` | TOML loading, extension matching, `Category()` (photos/videos) |
| `internal/scan` | Recursive file walk, `MissingFromDest()` / `MissingByRelPath()` comparison |
| `internal/copyop` | Atomic copy with `O_EXCL`, collision suffix (`_1`, `_2`…), batch runner |
| `internal/checksum` | SHA256 with optional progress writer |
| `internal/status` | Status command logic |
| `internal/verify` | Verify command logic, uses camera as authority (falls back to SSD) |
| `internal/ui` | Terminal colors, progress bar, `Prompt()`, `AskYesNo()`, `FreeSpace()` |

### Copy phase details

Phase 1 (Camera → SSD) transforms paths: `DCIM/DSC_0001.NEF` → `photos/2026-03-25/DSC_0001.NEF`
Phase 2 (SSD → NAS) preserves relative paths directly — no transformation.

This split lets the user disconnect and power off the camera between phases. Phase 2 is optional and skipped gracefully if NAS is unavailable.

Comparison uses filename + size (not hash) for speed. Collision: same name but different size is treated as a new file and saved with a `_N` suffix — the source is never modified.

### Key invariants

- Source files are never modified or deleted
- Destination files are created with `O_EXCL` — never overwritten
- Destination modtime is set to source modtime
- All extension and path comparisons are lowercased
- Free disk space is validated before any copy phase starts

### Platform-specific files

`internal/ui/freespace_windows.go` and `freespace_unix.go` implement `FreeSpace()` for each platform.
