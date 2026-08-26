# camera-backup

A CLI tool for safely backing up camera media (Nikon Z6 III and similar) from memory cards to a local SSD and a remote NAS — incrementally and with SHA256 verification. Cards and external drives can also be dumped straight to the NAS, skipping the local SSD.

Built in Go. Never deletes or overwrites source files.

---

## Workflow

### Daily backup (e.g. on vacation without reliable network)

1. Connect camera via USB-C (mounts as a drive, e.g. `/media/eric/NIKON`)
2. `camera-backup status` — see what needs copying and verify there is enough space
3. `camera-backup copy` — copies camera → SSD with SHA256 verification, then asks whether to continue to NAS (disconnect the camera first)
4. `camera-backup sync` — copies SSD → NAS when network is available (videos first); run overnight if needed
5. `camera-backup verify` — SHA256 check across all destinations; run after sync to confirm integrity
6. `camera-backup prune` — when the SSD fills up, free the space the NAS already holds

### At home / full sync

1. `camera-backup copy` — camera → SSD (verified) then SSD → NAS
2. `camera-backup verify` — confirm everything matches

### Straight to the NAS (no local SSD)

Plug in a memory card or an external SSD and push it to the NAS without a local
staging copy:

1. `camera-backup dump` — source → NAS, SHA256-verified
2. `camera-backup verify` — confirm everything matches

Set `direct_to_nas = true` in `config.toml` to make this the default for
`camera-backup copy` and for `[y]` in the TUI. See
[Dumping straight to the NAS](#dumping-straight-to-the-nas).

---

## Safety guarantees

- Source files are **never deleted** by this tool
- Source files are opened **read-only**
- The **one** command that deletes anything is `prune`, and only ever from the
  local SSD, only for files whose NAS copy it has just hashed and found
  identical, and only when asked with `--delete`
- Destination files are **never overwritten** — if a filename already exists, the new file is saved with a `_1`, `_2`, … suffix and a warning is printed
- Memory cards are always formatted manually in-camera
- Copy order is `Camera → SSD → NAS` by default. `camera-backup dump` (or
  `direct_to_nas = true`) copies the card straight to the NAS instead — those
  copies are always SHA256-verified, because the NAS copy is then the only copy
- `copy`, `dump` and `sync` check available disk space before starting and abort if there is not enough room

---

## Commands

### `camera-backup status`

Quick check — compares by shoot date, filename and file size rather than hashing. Shows how much data needs to be copied and whether there is enough free space on each destination.

A file already sitting under its own shoot date with a matching size counts as
backed up. When a copy turns up under a *different* date — an older backup, or a
file with no capture metadata whose timestamp was rewritten — the match is
confirmed against the capture time before the source is skipped, so two cards
that both number a frame `DSC_0001` are never confused with each other. Use
`camera-backup verify` when you want the full SHA256 comparison.

```
  Devices
  ────────────────────────────────────────────────────────
  ✅  Source      /media/eric/NIKON    (no free space info)
  ✅  SSD photos  /mnt/ssd/Photos      210.4 GB free
  ✅  SSD videos  /mnt/ssd/Videos      210.4 GB free
  ✅  NAS photos  /mnt/nas/Photos       1.2 TB free
  ✅  NAS videos  /mnt/nas/Videos       1.2 TB free

  Summary
  ────────────────────────────────────────────────────────
  Source files found :  47  (2.1 GB)
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

  [1/13] 2026/2026-03/2026-03-24/DSC_0142.NEF
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

### `camera-backup dump`

Copies files missing from the NAS straight from the source device, bypassing the
local SSD. Use it when you plug in a card or an external drive and want the
files on the NAS without a local staging copy.

```
camera-backup dump                    # all missing files, videos first
camera-backup dump --videos-only     # only video files (-v)
camera-backup dump --photos-only     # only photo files (-p)
camera-backup dump --order=size-asc  # smallest files first, regardless of type
```

Files land in the same `year/month/day` layout as a staged backup, so a card
dumped directly and a card copied via the SSD produce the same tree. Every file
is **SHA256-verified** after copying: with no SSD in the chain the NAS copy is
the only copy, so a failed run says plainly that the card is not safe to format.

The source device is the first mounted path of `source` / `extra_sources`, so
one config can serve a card reader and an external drive — see
[Dumping straight to the NAS](#dumping-straight-to-the-nas).

NAS writes are bounded by `nas_write_timeout_seconds` here too, so a hung mount
fails that one file instead of the whole run.

`dump` works whether or not `direct_to_nas` is set — the setting only decides
what `copy` and the TUI do by default.

### `camera-backup sync`

Copies files missing from NAS from the SSD. No camera required. By default videos are transferred before photos.

```
camera-backup sync                    # all missing files, videos first
camera-backup sync --videos-only     # only video files (-v)
camera-backup sync --photos-only     # only photo files (-p)
camera-backup sync --order=size-asc  # smallest files first, regardless of type
```

Use this when network becomes available after a `copy` run, or to push only videos when bandwidth is limited.

On a flaky or metered connection (e.g. a phone hotspot), small photo files are far more likely to complete before the link drops than multi-gigabyte videos. `--photos-only` pushes just the photos, and `--order=size-asc` sorts the whole batch by file size ascending so the most-likely-to-succeed files go first. Without `--order`, the order comes from the `nas_sync_order` config key (default: videos first, unchanged behaviour).

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

A destination that is not mounted is skipped rather than failed — the others are
still worth checking — but the result always says so, so a clean run can never
stand for something that was never looked at:

```
  All 47 files verified OK against the destinations that were checked.
  ⚠️  Not checked: NAS photos (/mnt/nas/Photos) — mount and re-run to verify there.
```

Two limits worth knowing. Verify is **source-driven**: it checks the files on
the authority (the card, or the SSD when no card is connected), so verifying one
card confirms that card's photos, not the whole NAS. And it only sees extensions
listed in `file_extensions` — the same blind spot `status` and `copy` have.

### `camera-backup prune`

Frees space on the staging SSD by deleting files the NAS already holds:

```
camera-backup prune                      # dry run: what could go, and what stays
camera-backup prune --older-than 14      # leave the last two weeks on the SSD
camera-backup prune --delete             # do it, after a confirmation
camera-backup prune --delete --yes       # do it without asking (scripts)
```

This is the only command that deletes anything, so it is deliberately narrow:

- Each candidate is decided by reading **both** copies in full and comparing
  SHA256 hashes. Names and sizes only decide what is worth hashing.
- A file is kept whenever anything is off — no copy on the NAS, a different
  size, a hash that disagrees, or a NAS root that is not mounted.
- A hash that disagrees is reported **loudly**: two files of the same size that
  hash differently mean one of the copies is damaged, which is worth knowing
  long before the SSD is cleared.
- Only the SSD is touched. The camera and the NAS are never written to.
- Nothing happens without `--delete`; a plain run prints the plan and stops.
- `--older-than` counts from the **capture date**, so "the last two weeks"
  means the last two weeks of shooting, not of file copying.
- Date directories left empty are removed; the configured roots are not.

```
  Prune plan
  ────────────────────────────────────────────────────
  Verified on NAS, safe to delete :  412  (38.4 GB)
  Dates                           :  2026-03-25 → 2026-07-02
  Kept, not on the NAS            :  18
  Kept, newer than the cutoff     :  96
```

### `camera-backup devices`

Lists the filesystems mounted right now — card readers, USB drives, internal
disks and network shares — so the mount point of a new card can be copied into
`config.toml` instead of hunted for with `lsblk`:

```
  Mounted devices
  ────────────────────────────────────────────────────────────────────────
  ● CAMERA-CARD (DCIM)   removable  /run/media/eric/CAMERA-CARD   46.1 GB free
    EXT-SSD              removable  /run/media/eric/EXT-SSD      412.0 GB free
  ○ NIKON-D850           removable  /run/media/eric/NIKON-D850    59.5 GB free
    photos               network    /mnt/nas                       2.1 TB free
    /                    internal   /                             94.3 GB free

  ● current source   ○ listed in config.toml
```

Removable devices come first, and a device holding a `DCIM` directory — a card
straight out of a camera — comes first of all. The device currently acting as
the source is marked `●`, the other devices named in `config.toml` `○`.

Discovery reads `/proc/self/mountinfo` and `/sys/class/block`; it is Linux-only,
like the rest of the tool. The TUI shows the same list on its device screen
(`d`), where a device can be picked outright instead of copied by hand.

### `camera-backup tui`

Interactive terminal UI wrapping all commands. Shows device availability, a
year → month → day file tree with per-file SSD/NAS sync status, and runs
copy/sync/verify with live parallel progress bars.

- Tabs: **All** / **Missing on SSD** / **Missing on NAS** (`tab` to switch)
- `j/k` or arrows navigate, `Enter` expands groups or previews a file
- `space` selects files (or whole groups), `a` selects all — `y` then copies
  only the selection (or everything when nothing is selected)
- `h`/`l` (or `←`/`→`) walk the tree: `l` opens a date group and steps into it,
  `h` closes it and steps back out — from a file, `h` closes the day it sits in
  and leaves the cursor there
- `[` / `]` jump between date groups without stepping over every frame — `[`
  from inside a day lands on that day's own row
- `z` folds the tree back to the list of years, `Z` unfolds it again, and `f`
  folds everything except the date under the cursor
- `g` opens a scrollable thumbnail grid for a date group — thumbnails load as
  you scroll, and `y` starts a copy directly from the grid
- Copies show per-file progress bars plus overall throughput and ETA;
  `q`/`esc` cancels gracefully (files in progress finish, the queue stops)
- `?` opens a help screen with all keybindings, `v` runs verify, `q` quits
- Image previews: JPEG directly; RAW (NEF, CR2, ARW, DNG, …) via `exiftool`
  (optional dependency) reading whichever preview the file embeds;
  full-screen previews use the Kitty Graphics Protocol in Ghostty/Kitty
- The Info panel draws the focused photograph the same way where the terminal
  speaks that protocol — block art spends one pixel per terminal column, which
  in a panel that narrow is barely a picture. `list_preview` in config.toml
  picks the mode; the panel also widens with the terminal (34 → 44 → 54
  columns)
- `c` opens a **settings screen** that edits config.toml in place — see below
- `d` opens a **device screen** listing everything mounted, so a different card
  or drive can be picked as the source mid-session — see below
- Devices are watched — plugging in the SD card (or any `extra_sources` device)
  refreshes the view automatically
- Copies to the NAS honour `nas_write_timeout_seconds` (a hung mount fails the
  file, not the batch) and `nas_sync_order` from config.toml
- With `direct_to_nas = true` the SSD column and tab disappear, the device
  header reads `Source → NAS`, and `y` dumps straight to the NAS (verified)

#### Device screen (`d`)

Swapping cards or drives normally means editing `source` in `config.toml` —
except when the new card happens to carry the same volume label as the old one
and lands on the same mount point. `d` removes the guesswork:

```
╭─ Devices ──────────────────────────────────────────────────────────────╮
│      DEVICE             TYPE        MOUNT POINT              FREE      │
│  ▶ ● CAMERA-CARD DCIM  removable   /run/media/eric/CAMERA…  46.1 GB fr…│
│    ○ EXT-SSD           removable   /run/media/eric/EXT-SSD  412.0 GB f…│
│    ○ photos            network     /mnt/nas                   2.1 TB f…│
│    ○ /                 internal    /                         94.3 GB f…│
│                                                                        │
│  /run/media/eric/CAMERA-CARD  ← /dev/sdb1 (vfat)                       │
│                                                                        │
│  [enter] use for this session · [s] also save to config.toml           │
╰────────────────────────────────────────────────────────────────────────╯
 [j/k] move  [enter] use as source  [s] save to config.toml  [r] refresh
```

- **`enter` swaps the source immediately.** The picked device becomes the
  source for this session and a fresh scan runs against the NAS — and the local
  SSD when it is in use — so the file tree, the tab counts and the ✔/✘ columns
  describe the new card within a second. `config.toml` is not touched: a card
  swapped mid-afternoon is a session choice, and the configured devices are
  still there when it comes out.
- **`s` also writes it to `config.toml`** as `source`, for the reader that is
  always in the same slot. The path `source` held moves into `extra_sources`
  rather than being dropped.
- The device in use is marked `●`, and the list opens with the cursor on it, so
  `enter` on an untouched list changes nothing.
- Plugging a device in or pulling one out while the screen is open refreshes the
  list by itself; `r` rescans on demand.
- The settings screen has the same list: `d` on **Source device** or **Extra
  sources** fills that field with a path from it instead of typing one.

#### Settings screen (`c`)

Every config.toml key can be edited without leaving the TUI — paths, the
`direct_to_nas` toggle, extension lists, worker counts, the write timeout and
the transfer order:

```
╭─ Settings • ───────────────────────────────────────────────────────────╮
│  ▶ Source device         /run/media/eric/NIKON              ✔ found    │
│    Extra sources         /run/media/eric/EXT-SSD            1/1 mounted│
│    Direct to NAS         [✔] on                                        │
│    SSD photos            (not set)                          unset      │
│    NAS photos            /mnt/nas/Photos                    ✔ found    │
│    NAS transfer order    videos-first                                  │
│                                                                        │
│  direct_to_nas — bypass the local SSD — dump straight to the NAS       │
│  Unsaved changes                                                       │
╰────────────────────────────────────────────────────────────────────────╯
 [j/k] move  [enter] edit/toggle  [d] devices  [s] save  [r] reload  [esc] back
```

- `j/k` moves, `enter` (or `space`) edits a path, flips a toggle, or cycles an
  enum. While editing: `←/→`, `home`/`end`, `ctrl+u` to clear, `enter` to
  accept, `esc` to cancel
- **Paths are probed as you type** — `✔ found` / `✘ missing` updates on every
  keystroke, so a typo in a mount point is obvious before you save. Destination
  roots count as found when their parent exists, since the root itself is
  created on the first copy
- `s` saves. The edit is validated first (the same rules `config.Load` applies),
  so an impossible combination — dropping the SSD roots without turning on
  `direct_to_nas`, say — is reported and **nothing is written**
- After a save the TUI adopts the new config immediately: devices are rescanned
  against the new paths and the device watcher follows them. No restart
- `r` reloads from disk, discarding edits. Leaving with unsaved changes takes a
  second `esc` (the title shows `Settings •` while dirty)

Saving is a **surgical rewrite** of config.toml: each key is updated on the line
where it already sits, so your comments, ordering and any keys this tool does
not manage survive untouched. Keys the file did not have are appended under an
`# Added by camera-backup` heading. The write is atomic, so an interrupted
save cannot leave a truncated config.

Optional keys are written as their effective values — after the first save the
file states explicitly what the tool is doing rather than relying on defaults.

---

## Configuration

Copy `config-template.toml` to `config.toml` next to the binary (or pass
`--config <path>`) and adjust the paths. `config.toml` is gitignored, so your
local paths stay out of version control.

Everything below can also be edited from the TUI's settings screen (`c`) instead
of by hand — see [Settings screen](#settings-screen-c). To find the mount point
of a card or drive, run `camera-backup devices` or pick it from the TUI's device
screen (`d`) — see [Device screen](#device-screen-d).

```toml
source     = "/media/eric/NIKON"      # Camera / memory card (mount point)
extra_sources = ["/media/eric/EXT-SSD"] # More source devices, tried in order
                                       # when `source` is not mounted (optional)
direct_to_nas = false                 # true = dump source → NAS, skipping the
                                       # local SSD (optional, default false)
ssd_photos = "/mnt/ssd/Photos"        # SSD destination for photos
ssd_videos = "/mnt/ssd/Videos"        # SSD destination for videos
nas_photos = "/mnt/nas/Photos"        # NAS destinations (optional, set both or neither)
nas_videos = "/mnt/nas/Videos"

file_extensions  = [".MOV", ".NEF", ".JPG", ".MP4"]
video_extensions = [".MOV", ".MP4"]   # these route to the videos destination
                                       # everything else goes to photos

# Parallel copy workers used by the TUI (optional)
ssd_workers = 3               # camera → SSD (default 3)
nas_workers = 1               # SSD → NAS   (default 1)

# Per-file write timeout for SSD → NAS copies (optional, default 60)
# Guards against hung network mounts: a hard-mounted NFS/CIFS share blocks
# forever when the connection drops instead of returning an error. When a
# file hits this timeout it is counted as failed and the sync moves on to
# the next file. Applies to both the CLI and the TUI.
# See "Recommended NAS mount options" below.
nas_write_timeout_seconds = 60

# How the TUI's Info panel draws the focused photograph (optional):
# "auto" (default), "kitty", "blocks" or "off" — see config-template.toml
list_preview = "auto"

# SSD → NAS transfer order (optional): "videos-first" (default) or "size-asc"
# (smallest files first — most likely to complete on a flaky connection).
# Used by the TUI and as the default for `sync --order`.
nas_sync_order = "videos-first"
```

Photos and videos each have their own destination directory per device. Point
both keys at the **same** path to merge them into one tree. A file's category
is always decided by its extension, so a merged SSD can still be split onto
separate NAS directories (and vice versa). If one category's directory is
unavailable (e.g. the video disk isn't mounted), the other category is still
copied and the skipped files are reported.

Extensions are matched **case-insensitively** — `.NEF`, `.nef` and `.Nef` all match.

### Dumping straight to the NAS

`direct_to_nas = true` takes the local SSD out of the copy path: plug in a
memory card or an external SSD, and `camera-backup copy` — or `[y]` in the TUI —
copies it straight to the NAS.

```toml
source        = "/run/media/eric/NIKON"        # the card reader
extra_sources = ["/run/media/eric/EXT-SSD"]    # …or an external SSD
nas_photos    = "/mnt/nas/Photos"
nas_videos    = "/mnt/nas/Videos"
direct_to_nas = true
```

- **Multiple source devices.** The source is the first path in
  `source` + `extra_sources` that is actually mounted, so whichever device you
  plug in becomes the source. Nothing else needs to change between a card and
  an external drive. A device that is in neither list — a card whose label puts
  it somewhere new — is picked from the TUI's device screen (`d`) without
  editing anything.
- **Always verified.** Direct copies are SHA256-verified, because the NAS copy
  is the only copy. If a file fails, the run says so and tells you not to format
  the card.
- **Same layout.** Files land in the usual `year/month/day` tree under the NAS
  category roots, so a direct dump and a staged backup are interchangeable.
  Because the destination path comes from each file's capture date, an external
  drive that already holds a `year/month/day` tree lands in the same places on
  the NAS — even if copying the files onto that drive reset their timestamps.
- **The SSD keys become optional.** Leave `ssd_photos`/`ssd_videos` out
  entirely, or keep them so `camera-backup sync` can still push an existing SSD
  tree to the NAS. Either way, `copy` and the TUI bypass the SSD, and the SSD
  is shown as *bypassed* in `status` rather than missing.
- **Per-run instead of permanent.** Leave the setting off and run
  `camera-backup dump` when you want a direct copy — the setting only changes
  the default for `copy` and the TUI.

Only `direct_to_nas` needs `nas_photos`/`nas_videos` to be set; without a NAS
there is nowhere to dump to and the config is rejected at load time.

### Recommended NAS mount options

When syncing over an unstable connection (e.g. a phone hotspot tethered to the
laptop, WireGuard back to the NAS at home), the way the share is mounted
matters more than anything this tool can do.

By default, NFS uses a **hard** mount: if the connection drops, every read and
write against the mount **blocks indefinitely** until the server comes back —
it never returns an error. That is the right behaviour for data integrity on a
LAN, but on a flaky link it means `sync` (and anything else touching the
mount, including `ls`) freezes with no feedback. The
`nas_write_timeout_seconds` setting above ensures the sync itself moves on to
the next file, but the stalled write still holds a partial file on the NAS
until the mount recovers — fixing it at the mount level is better.

For **NFS**, use a soft mount with a bounded retry budget:

```
# /etc/fstab
nas:/volume1/photos  /mnt/nas/Photos  nfs  soft,timeo=100,retrans=3,_netdev  0  0
```

- `soft` — return an I/O error to the application instead of blocking forever
- `timeo=100` — wait 10 seconds (deciseconds) before each retransmission
- `retrans=3` — give up after 3 retries, so a dead link fails in ~30–60 s

`soft` can, in theory, cause silent data corruption on writes that error
mid-flight — which is exactly why this tool never trusts the fast SSD→NAS copy
and `verify` re-hashes everything afterwards. Failed files are simply
re-copied on the next `sync`.

For **CIFS/SMB**, the equivalent is:

```
//nas/photos  /mnt/nas/Photos  cifs  soft,echo_interval=10,credentials=/etc/nas-creds,_netdev  0  0
```

- `soft` — same semantics as NFS: error out instead of hanging
- `echo_interval=10` — detect a dead server after ~2×10 s of silence

With a hard mount (or the `hard` default), keep `nas_write_timeout_seconds`
low so a hang costs one timeout per file instead of a frozen terminal.

---

## Directory structure on destination

Files are organised by shoot date in a year → month → day hierarchy directly under each category's destination directory. The DCIM folder structure from the camera is not preserved — filenames are kept flat under the day folder.

The shoot date is read from the file's own metadata: the EXIF capture date for
photos (JPEG and RAW — NEF, CR2, ARW, DNG, ORF, RW2, RAF), the recording time
from the movie header for MP4/MOV. The file's modification time is used only when
a file carries no such metadata.

This matters as soon as the files reach you via anything other than the camera.
Copying a card to an external drive with a file manager, restoring a backup or
unpacking an archive stamps every file with *now* — filing by modification time
would bury a whole shoot under the date you happened to copy it. Reading the
capture date puts each shot under the day it was taken, whatever the file
timestamps say. No external tool is needed for this; the metadata is parsed
directly.

```
/mnt/ssd/Photos/                      /mnt/ssd/Videos/
  2026/                                 2026/
    2026-03/                              2026-03/
      2026-03-24/                           2026-03-24/
        DSC_0001.NEF                          VIDEO001.MOV
        DSC_0001.JPG                          VIDEO002.MP4
        DSC_0002.NEF
```

Both SSD and NAS use the same structure, whether the files arrive via the SSD or
straight from the card with `dump`. The date folder prevents filename collisions
across sessions (Nikon resets to `DSC_0001` when a new card is formatted).

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

`testdata/config-direct.toml` exercises the direct source → NAS path
(`direct_to_nas = true`, no `ssd_*` keys) against the same testdata:

```bash
go run ./cmd/camera-backup --config testdata/config-direct.toml status
go run ./cmd/camera-backup --config testdata/config-direct.toml dump
go run ./cmd/camera-backup --config testdata/config-direct.toml verify -v
```

It also lists `testdata/extdrive` in `extra_sources`; create that directory
with a file in it to check that the source falls back to a second device.

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

Copy the `camera-backup` binary and your `config.toml` (created from
`config-template.toml`) to a directory of your choice and run from any terminal. For RAW previews in the TUI, install `exiftool` (`sudo apt install libimage-exiftool-perl`).

---

## Project structure

```
camera-backup/
├── cmd/camera-backup/
│   └── main.go              # Entry point, subcommands, space check
├── internal/
│   ├── config/              # TOML loading, extension matching, worker counts
│   ├── devices/             # Mounted-device discovery (mountinfo + sysfs)
│   ├── prune/               # Delete SSD files whose NAS copy hashes identical
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
├── config-template.toml     # Configuration template (copy → config.toml)
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
previews from RAW files (.NEF, .CR2, .ARW, .DNG, …). Without it, RAW files show
metadata only and the preview panel says so. Only previews depend on it; capture
dates are read without any external tool.

On Debian/Ubuntu: `sudo apt install libimage-exiftool-perl`.

---

## License

MIT
