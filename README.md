# camera-backup

A CLI tool for safely backing up camera media (Nikon Z6 III and similar) from memory cards to a local SSD and a remote NAS — incrementally and with SHA256 verification.

Built in Go. Never deletes or overwrites source files.

---

## Workflow

### Daily backup (e.g. on vacation without reliable network)

1. Connect camera via USB-C (mounts as a drive, e.g. `/media/eric/NIKON`)
2. `camera-backup status` — see what needs copying and verify there is enough space
3. `camera-backup copy` — copies camera → SSD with SHA256 verification, then asks whether to continue to NAS (disconnect the camera first)
4. `camera-backup sync` — copies SSD → NAS when network is available (videos first); run overnight if needed
5. `camera-backup verify` — SHA256 check across all destinations; run after sync to confirm integrity

### At home / full sync

1. `camera-backup copy` — camera → SSD (verified) then SSD → NAS
2. `camera-backup verify` — confirm everything matches

---

## Safety guarantees

- Source files are **never deleted** by this tool
- Source files are opened **read-only**
- Destination files are **never overwritten** — if a filename already exists, the new file is saved with a `_1`, `_2`, … suffix and a warning is printed
- Memory cards are always formatted manually in-camera
- Copy order is always `Camera → SSD → NAS` (never camera → NAS directly)
- `copy` checks available disk space before starting and aborts if there is not enough room

---

## Commands

### `camera-backup status`

Quick check — compares by filename and file size. Shows how much data needs to be copied and whether there is enough free space on each destination.

```
  Devices
  ────────────────────────────────────────────────────────
  ✅  Camera  /media/eric/NIKON        (no free space info)
  ✅  SSD     /mnt/ssd/CameraBackup    210.4 GB free
  ✅  NAS     /mnt/nas/CameraBackup     1.2 TB free

  Summary
  ────────────────────────────────────────────────────────
  Camera files found :  47  (2.1 GB)
  Missing from SSD   :  13  (620.4 MB to copy, 210.4 GB free)
  Missing from NAS   :  13  (620.4 MB to copy, 1.2 TB free)
```

If a destination is not connected it shows as `not available` in red.

### `camera-backup copy`

Phase 1 copies camera → SSD with a 4 MB buffer, `fsync`, and SHA256 verification after each file (SSD is the source of truth). Phase 2 copies SSD → NAS quickly without per-file verification — run `verify` afterwards to confirm integrity.

```
  Phase 1: Camera → SSD
  ─────────────────────────────────────────

  Copying 13 file(s) to SSD...

  [1/13] photos/2026-03-24/DSC_0142.NEF
  DSC_0142.NEF               45.2 MB   89.3 MB/s  [████████████████████]  100.0%
    Verifying DSC_0142.NEF              ✅
  ...

  ✅  13 file(s) copied and verified.

════════════════════════════════════════════════════════════

  Camera backup to SSD is complete.
  You may now disconnect and power off the camera.

  Continue to sync SSD → NAS? [y/n]:

════════════════════════════════════════════════════════════

  SSD → NAS
  ─────────────────────────────────────────

  Copying 13 file(s) to NAS (videos first)...
  ...

  ✅  13 file(s) copied.
```

If the NAS is not reachable (VPN down, drive not mapped), the tool exits cleanly after Phase 1. Run `camera-backup sync` later to push to NAS — files already there are skipped automatically.

### `camera-backup sync`

Copies files missing from NAS from the SSD. No camera required. Videos are always transferred before photos.

```
camera-backup sync              # all missing files, videos first
camera-backup sync --videos-only  # only video files
camera-backup sync -v             # shorthand
```

Use this when network becomes available after a `copy` run, or to push only videos when bandwidth is limited.

### `camera-backup verify`

Deep integrity check — reads every file and computes SHA256. Slow but thorough. Run after `sync` or monthly.

- If camera is connected: verifies camera vs SSD vs NAS
- If camera is not connected: verifies SSD vs NAS (SSD is used as authority)

By default only failures are printed:

```
  Verifying 47 files...

  ⚠️   DSC_0098.NEF — [missing from NAS]
  ⚠️   VIDEO003.MOV — [SSD hash mismatch]

  2 / 47 files have issues.
```

Pass `--verbose` / `-v` to see every file.

### `camera-backup tui`

Interactive terminal UI wrapping all commands. Shows device availability, a
year → month → day file tree with per-file SSD/NAS sync status, and runs
copy/sync/verify with live parallel progress bars.

- Tabs: **All** / **Missing on SSD** / **Missing on NAS** (`tab` to switch)
- `j/k` or arrows navigate, `Enter` expands groups or previews a file
- `space` selects files (or whole groups), `a` selects all — `y` then copies
  only the selection (or everything when nothing is selected)
- `g` opens a scrollable thumbnail grid for a date group — thumbnails load as
  you scroll, and `y` starts a copy directly from the grid
- Copies show per-file progress bars plus overall throughput and ETA;
  `q`/`esc` cancels gracefully (files in progress finish, the queue stops)
- `?` opens a help screen with all keybindings, `v` runs verify, `q` quits
- Image previews: JPEG directly; NEF via `exiftool` (optional dependency);
  full-screen previews use the Kitty Graphics Protocol in Ghostty/Kitty
- Devices are watched — plugging in the SD card refreshes the view automatically

---

## Configuration

Place `config.toml` next to the binary, or pass `--config <path>`.

```toml
source = "/media/eric/NIKON"        # Camera / memory card (mount point)
ssd    = "/mnt/ssd/CameraBackup"    # Local SSD destination
nas    = "/mnt/nas/CameraBackup"    # NAS mounted via SMB/NFS (or WireGuard VPN)

file_extensions  = [".MOV", ".NEF", ".JPG", ".MP4"]
video_extensions = [".MOV", ".MP4"]   # sorted into videos/ on destination
                                       # everything else goes into photos/

# Parallel copy workers used by the TUI (optional)
ssd_workers = 3               # camera → SSD (default 3)
nas_workers = 1               # SSD → NAS   (default 1)
```

Extensions are matched **case-insensitively** — `.NEF`, `.nef` and `.Nef` all match.

---

## Directory structure on destination

Files are organised by category and shoot date (taken from the file's modification time) in a year → month → day hierarchy. The DCIM folder structure from the camera is not preserved — filenames are kept flat under the day folder.

```
/mnt/ssd/CameraBackup/
  photos/
    2026/
      2026-03/
        2026-03-24/
          DSC_0001.NEF
          DSC_0001.JPG
          DSC_0002.NEF
  videos/
    2026/
      2026-03/
        2026-03-24/
          VIDEO001.MOV
          VIDEO002.MP4
```

Both SSD and NAS use the same structure. The date folder prevents filename collisions across sessions (Nikon resets to `DSC_0001` when a new card is formatted).

---

## Logs

Each run produces a timestamped log file in `logs/` next to the binary.

```
logs/
  2026-03-24_21-05-42.log
  2026-03-24_22-13-10.log
```

Logs include: files copied, SHA256 checksums, errors and run summary. If a filename collision is resolved by renaming, a `COLLISION` entry is written with both the original and the saved path.

---

## Local testing

Synthetic testdata covering all copy scenarios can be generated with:

```bash
go run testdata/make_testdata.go
```

Then run against it:

```bash
go run ./cmd/camera-backup --config testdata/config.toml status
go run ./cmd/camera-backup --config testdata/config.toml copy
go run ./cmd/camera-backup --config testdata/config.toml verify -v
```

Reset:

```bash
rm -rf testdata/camera testdata/ssd testdata/nas && go run testdata/make_testdata.go
```

---

## Installation

Requires Go 1.26+. Linux only.

```bash
git clone https://github.com/Eric-Eklund/camera-backup
cd camera-backup
go build -o camera-backup ./cmd/camera-backup
```

Copy the `camera-backup` binary and `config.toml` to a directory of your choice and run from any terminal. For RAW (.NEF) previews in the TUI, install `exiftool`.

---

## Project structure

```
camera-backup/
├── cmd/camera-backup/
│   └── main.go              # Entry point, subcommands, space check
├── internal/
│   ├── config/              # TOML loading, extension matching, worker counts
│   ├── scan/                # File scanning and comparison
│   ├── checksum/            # SHA256 calculation
│   ├── copyop/              # Copy with progress + verification (serial + parallel)
│   ├── status/              # status command + Compute() for the TUI
│   ├── verify/              # verify command + RunWithCallback() for the TUI
│   ├── preview/             # Thumbnails (exiftool), block art, Kitty graphics
│   ├── tui/                 # Interactive TUI (bubbletea)
│   └── ui/                  # Terminal colours, progress bar, prompts
├── testdata/
│   ├── config.toml          # Config pointing at testdata directories
│   ├── make_testdata.go     # Generator for synthetic test files
│   └── .gitignore
├── config.toml              # User configuration (edit this)
├── go.mod
└── go.sum
```

---

## Dependencies

| Package | Purpose |
|---|---|
| `github.com/BurntSushi/toml` | TOML config parsing |
| `github.com/spf13/cobra` | CLI subcommands |
| `github.com/fatih/color` | Terminal colours |
| `github.com/charmbracelet/bubbletea` | TUI framework |
| `github.com/charmbracelet/lipgloss` | TUI styling |
| `github.com/fsnotify/fsnotify` | Device mount watching in the TUI |
| `golang.org/x/image` | Thumbnail scaling |

Optional runtime dependency: **exiftool** — used by the TUI to extract embedded
previews from RAW (.NEF) files. Without it, RAW files show metadata only.

---

## License

MIT
