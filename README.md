# camera-backup

A CLI tool for safely backing up camera media (Nikon Z6 III and similar) from memory cards to a local SSD and a remote NAS — incrementally and with SHA256 verification.

Built in Go. Never deletes source files.

---

## Overview

**Typical workflow on vacation:**

1. Connect camera via USB-C (mounts as a drive, e.g. `E:\`)
2. Run `camera-backup status` to see what needs copying
3. Run `camera-backup copy`
   - Copies new files from camera → SSD
   - SHA256 verification after each file
   - **Pauses and prompts** — disconnect and power off camera here
   - Continues copying SSD → NAS
   - SHA256 verification after each file
4. Run `camera-backup status` again as a final safety check before formatting cards in-camera

---

## Safety guarantees

- Source files (camera/card) are **never deleted** by this tool
- Source files are opened **read-only**
- Memory cards are always formatted manually in-camera
- Copying order is always: `Camera → SSD → NAS` (never camera → NAS directly)

---

## Commands

### `camera-backup status`

Checks device availability and compares what exists where.

- Shows ✅ / ❌ for each configured device (camera, SSD, NAS)
- Shows free space on SSD and NAS
- Scans all matching files on the camera
- Compares against SSD and NAS (by filename + file size)
- Displays a file-by-file table showing where each file exists
- Summarises how many files need to be copied to SSD / NAS

### `camera-backup copy`

Incremental copy with verification.

1. Copy missing files: Camera → SSD
2. SHA256 verify each copied file (source vs copy)
3. **Prompt:** "Camera backup complete. You may now disconnect the camera. Press Enter to continue to NAS..."
4. Copy missing files: SSD → NAS
5. SHA256 verify each copied file (SSD vs NAS)

Shows filename, size and transfer speed during copy. Logs everything.

### `camera-backup verify`

Deep integrity check across all three locations.

- Calculates SHA256 for every file on camera, SSD and NAS
- Compares checksums
- Reports ✅ (match on all three) or ⚠️ (missing or mismatch) per file

---

## Configuration

Place `config.toml` in the same directory as the binary (or pass `--config` flag).

```toml
source = "E:\\"               # Camera / memory card (mounted drive)
ssd    = "D:\\CameraBackup"   # Local SSD destination
nas    = "Y:\\CameraBackup"   # NAS mapped via SMB (or WireGuard VPN)

file_extensions = [
  ".MOV",
  ".NEF",
  ".JPG",
  ".MP4",
]
```

File extensions are matched **case-sensitively**.

---

## Directory structure on destination

The folder structure from the camera is preserved on both SSD and NAS. Nikon cameras typically store files under `DCIM/100NIKON/`, `DCIM/101NIKON/` etc.

```
D:\CameraBackup\
  DCIM\
    100NIKON\
      DSC_0001.NEF
      DSC_0001.JPG
    101NIKON\
      ...
```

---

## Logs

Each run produces a timestamped log file in the `logs/` directory next to the binary.

```
logs/
  2026-03-24_21-05-42.log
  2026-03-24_22-13-10.log
```

Logs include: files copied, checksums, transfer speeds, errors and run summary.

---

## Installation

### Build from source

Requires Go 1.22+.

```bash
git clone https://github.com/Eric-Eklund/camera-backup
cd camera-backup
go build -o camera-backup.exe ./cmd/camera-backup
```

### Windows

Copy `camera-backup.exe` and `config.toml` to a folder on your laptop. Run from PowerShell or Windows Terminal.

---

## Project structure

```
camera-backup/
├── cmd/
│   └── camera-backup/
│       └── main.go          # Entry point, subcommands
├── internal/
│   ├── config/              # TOML loading and validation
│   ├── scan/                # File scanning and comparison
│   ├── checksum/            # SHA256 calculation
│   ├── copyop/              # Copy operations and verification
│   ├── status/              # Status command logic
│   ├── verify/              # Deep verification logic
│   └── ui/                  # Terminal colours, tables, progress
├── logs/                    # One log file per run (timestamped)
├── config.toml              # User configuration
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
