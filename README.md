# camera-backup

A CLI tool for safely backing up camera media (Nikon Z6 III and similar) from memory cards to a local SSD and a remote NAS — incrementally and with SHA256 verification.

Built in Go. Never deletes source files.

---

## Workflow

1. Connect camera via USB-C (mounts as a drive, e.g. `E:\`)
2. `camera-backup status` — see what needs copying
3. `camera-backup copy`
   - Copies new files camera → SSD, SHA256 verifies each file
   - **Pauses** — disconnect and power off camera here
   - Copies SSD → NAS, SHA256 verifies each file
4. `camera-backup status` — final check before formatting cards in-camera
5. `camera-backup verify` — run occasionally to detect silent corruption

---

## Safety guarantees

- Source files are **never deleted** by this tool
- Source files are opened **read-only**
- Memory cards are always formatted manually in-camera
- Copy order is always `Camera → SSD → NAS` (never camera → NAS directly)

---

## Commands

### `camera-backup status`

Quick check — compares by filename and file size. Runs in seconds regardless of how many files are on the card.

```
  Devices
  ────────────────────────────────────────────────────────
  ✅  Camera  E:\                      (no free space info)
  ✅  SSD     D:\CameraBackup          210.4 GB free
  ✅  NAS     Y:\CameraBackup           1.2 TB free

  Summary
  ────────────────────────────────────────
  Camera files found :  47
  Missing from SSD  :  13
  Missing from NAS  :  13
```

### `camera-backup copy`

Incremental copy with SHA256 verification after each file.

```
  Phase 1: Camera → SSD
  ─────────────────────────────────────────

  Copying 13 file(s) to SSD...

  [1/13] photos/2026-03-24/DSC_0142.NEF
  DSC_0142.NEF               45.2 MB   89.3 MB/s  [████████████████████]  100.0%
    Verifying DSC_0142.NEF              ✅

  [2/13] photos/2026-03-24/DSC_0142.JPG
  DSC_0142.JPG                8.1 MB   91.0 MB/s  [████████████████████]  100.0%
    Verifying DSC_0142.JPG              ✅
  ...

  ✅  13 file(s) copied and verified.

════════════════════════════════════════════════════════════

  Camera backup to SSD is complete.
  You may now disconnect and power off the camera.

  Press Enter when ready to continue to NAS...

════════════════════════════════════════════════════════════

  Phase 2: SSD → NAS
  ─────────────────────────────────────────

  Copy 13 file(s) to NAS? [y/N]: y

  [1/13] photos/2026-03-24/DSC_0142.NEF
  ...

  ✅  13 file(s) copied and verified.
```

If the NAS is not reachable (VPN down, drive not mapped), the tool exits cleanly after Phase 1. Re-running `copy` later will skip files already on the SSD.

### `camera-backup verify`

Deep integrity check — reads every file and computes SHA256. Slow but thorough. Run monthly or after moving drives.

By default only failures are printed:

```
  Verifying 47 files...

  ⚠️   DSC_0098.NEF — [missing from NAS]
  ⚠️   VIDEO003.MOV — [SSD hash mismatch]

  2 / 47 files have issues.
```

Pass `--verbose` / `-v` to see every file:

```
camera-backup verify --verbose
```

```
  ✅  DSC_0142.NEF
  ✅  DSC_0142.JPG
  ⚠️   DSC_0098.NEF — [missing from NAS]
  ...
```

---

## Configuration

Place `config.toml` next to the binary, or pass `--config <path>`.

```toml
source = "E:\\"               # Camera / memory card (mounted drive)
ssd    = "D:\\CameraBackup"   # Local SSD destination
nas    = "Y:\\CameraBackup"   # NAS mapped via SMB (or WireGuard VPN)

file_extensions  = [".MOV", ".NEF", ".JPG", ".MP4"]
video_extensions = [".MOV", ".MP4"]   # sorted into videos/ on destination
                                       # everything else goes into photos/
```

Extensions are matched **case-insensitively** — `.NEF`, `.nef` and `.Nef` all match.

---

## Directory structure on destination

Files are organised by category and shoot date (taken from the file's modification time). The DCIM folder structure from the camera is not preserved — filenames are kept flat under the date folder.

```
D:\CameraBackup\
  photos\
    2026-03-24\
      DSC_0001.NEF
      DSC_0001.JPG
      DSC_0002.NEF
  videos\
    2026-03-24\
      VIDEO001.MOV
      VIDEO002.MP4
```

Both SSD and NAS use the same structure. This prevents filename collisions across sessions (Nikon resets to `DSC_0001` when a new card is formatted).

---

## Logs

Each run produces a timestamped log file in `logs/` next to the binary.

```
logs/
  2026-03-24_21-05-42.log
  2026-03-24_22-13-10.log
```

Logs include: files copied, SHA256 checksums, errors and run summary.

---

## Installation

Requires Go 1.22+.

```bash
git clone https://github.com/Eric-Eklund/camera-backup
cd camera-backup
go build -o camera-backup.exe ./cmd/camera-backup
```

Copy `camera-backup.exe` and `config.toml` to a folder on your laptop. Run from PowerShell or Windows Terminal.

---

## Project structure

```
camera-backup/
├── cmd/camera-backup/
│   └── main.go              # Entry point, subcommands
├── internal/
│   ├── config/              # TOML loading, extension matching
│   ├── scan/                # File scanning and comparison
│   ├── checksum/            # SHA256 calculation
│   ├── copyop/              # Copy with progress + verification
│   ├── status/              # status command
│   ├── verify/              # verify command
│   └── ui/                  # Terminal colours, progress bar, prompts
├── testdata/
│   ├── config.toml          # Config pointing at testdata directories
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

---

## License

MIT
