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

# Direct Source → NAS path (direct_to_nas = true, no ssd_* keys)
go run ./cmd/camera-backup --config testdata/config-direct.toml status
go run ./cmd/camera-backup --config testdata/config-direct.toml dump
go run ./cmd/camera-backup --config testdata/config-direct.toml tui
```

## Architecture

Three-stage incremental backup pipeline: **Camera → SSD → NAS**, plus a
**direct Source → NAS** path that bypasses the local SSD (`dump`, or
`direct_to_nas = true`).

Seven subcommands (Cobra CLI):
- `status` — scans all three destinations and shows missing file counts + free space
- `copy` — Camera→SSD (CopyAndVerify) then SSD→NAS (fast Copy); runs `runDirect` instead when `direct_to_nas` is set
- `dump` — Source→NAS directly (CopyAndVerify), no local SSD; same `--videos-only/-v`, `--photos-only/-p` and `--order` flags as `sync`. Always available, regardless of `direct_to_nas`
- `sync` — SSD→NAS only, no camera required; `--videos-only/-v` and `--photos-only/-p` filter by category (mutually exclusive); `--order=size-asc` sorts the batch smallest-first; default order comes from `nas_sync_order` (videos first when unset)
- `verify` — deep SHA256 check; uses camera as authority, falls back to SSD if camera absent
- `devices` — lists mounted filesystems that could be a source, marking the active one
- `tui` — interactive bubbletea TUI wrapping all of the above with parallel copy workers

**Package responsibilities:**

| Package | Role |
|---|---|
| `cmd/camera-backup` | CLI entry point, `runCopy()` + `runSync()` + `runDirect()` orchestration, logging init |
| `internal/config` | TOML loading + `Validate()` + `Save()` (comment-preserving write-back), extension matching, `Category()` (photos/videos), worker counts, `ActiveSource()`, `SetSourceOverride()`, `SSDInUse()` |
| `internal/devices` | Linux mounted-device discovery (`/proc/self/mountinfo` + `/sys/class/block`) for the source picker |
| `internal/scan` | Recursive file walk, `MissingFromDest()` / `MissingByRelPath()` comparison |
| `internal/copyop` | `CopyAndVerify` (Sync+SHA256; Camera→SSD and direct Source→NAS), `Copy` (fast, SSD→NAS), `RunBatch(verify bool)`, `RunBatchParallel()` (TUI worker pool with `FileProgress` events) |
| `internal/checksum` | SHA256 with optional progress writer |
| `internal/status` | Status command logic; `Compute()` returns `StatusResult` for the TUI |
| `internal/verify` | Verify command logic; `RunWithCallback()` streams per-file results to the TUI |
| `internal/preview` | Thumbnails (JPEG direct, RAW via exiftool), ANSI block art, Kitty Graphics Protocol; `scaleImage` halves large frames before the resampling kernel (`prescale`) |
| `internal/tui` | bubbletea Model/Update/View, ops launchers, fsnotify device watcher, settings screen (`settings.go`) |
| `internal/ui` | Terminal colors, progress bar, `Prompt()`, `AskYesNo()`, `FreeSpace()` |

### Source devices

`source` plus the optional `extra_sources` list form an ordered set of source
candidates; `config.ActiveSource()` returns the first one that exists as a
directory (falling back to `source` so messages name a real path). Everything
downstream — `status.Compute`, `verify`, `runCopy`, `runDirect` — resolves the
source through it rather than reading `cfg.Source`, so plugging in either a card
reader or an external drive just works. The TUI's fsnotify watcher watches the
parent of every candidate.

That list only helps for a device whose mount point is already written down. A
card's mount point is `/run/media/<user>/<VOLUME-LABEL>`, so a card with a label
nobody has seen before lands somewhere the config does not name — which is what
`internal/devices` and the TUI's device screen are for.

- `devices.List()` parses `/proc/self/mountinfo` (not `/proc/mounts`: mount
  points are named after volume labels, which can contain spaces) and keeps
  only an **allowlist** of real filesystems — pseudo filesystems (proc, sysfs,
  cgroup, overlay, every snap's squashfs) are never where photographs live, and
  new ones keep appearing, so a denylist would rot.
- How a device is attached comes from sysfs: the disk's `removable` flag, or
  failing that the bus in its resolved `/sys/devices` path (`/usb`,
  `/mmc_host/` — a PCIe SD slot reports `removable = 0`).
- Free space and the `DCIM` probe both touch the filesystem, which a dead
  network mount can block on indefinitely. Each device is therefore probed in
  its own goroutine behind a 2 s deadline and listed without sizes if it misses
  it — the picker must not hang on an unrelated share.
- Ordering is the picker's whole value: DCIM first, then anything mounted under
  `/run/media` or `/media` (the desktop mounted it for this session), then
  removable, network, and fixed disks.
- `config.SetSourceOverride()` is how a picked device takes effect: it goes in
  front of `SourceCandidates()` for this run only. It is deliberately **not** a
  TOML key — swapping cards several times an afternoon must not rewrite
  config.toml — and it still falls back to the configured devices when the
  picked one is unplugged. Saving the settings screen clears it, since
  config.toml then answers the same question explicitly.

### Direct Source → NAS mode

`direct_to_nas = true` takes the local SSD out of the copy path: `copy` and the
TUI's `y` run a single verified Source→NAS pass. `dump` does the same on demand
without the setting. Notes:

- Destination paths use `DestRelPath()` (year/month/day), the same transform as
  Camera→SSD — so a direct dump and a staged backup produce identical trees, and
  an external drive that already holds a date tree lands in the same places.
  Comparison is therefore `MissingFromDest` (date keys), not `MissingByRelPath`.
- Direct copies are **always verified** (`CopyAndVerify` / `doVerify: true`) —
  the NAS copy is the only copy. They also honour `nas_write_timeout_seconds`.
- `ssd_photos`/`ssd_videos` become optional, but a lone key is still rejected.
  `direct_to_nas` requires the NAS keys. `cfg.SSDInUse()` (configured **and** not
  direct) is what UI code should ask before showing SSD state: in direct mode
  `status` reports the SSD as *bypassed*, `MissingOnSSD` is not computed, and the
  TUI hides the SSD badge, the ✓SSD column and the "Missing on SSD" tab.
- `sync` still works in direct mode when SSD roots are configured — it is an
  explicit SSD→NAS request.

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

A direct dump (Source → NAS) uses the Phase 1 transform against the NAS roots — see "Direct Source → NAS mode" above.

### Which date a file is filed under

`DestRelPath()` uses `FileInfo.DateTaken()`: the **capture time** when the file
carries one, the filesystem modtime otherwise. The modtime alone is wrong for a
source that was written by something other than the camera — copying a card to
an external drive with a file manager stamps every file with "now", which would
bury a whole shoot under the date it was copied.

- `scan.CaptureTime()` parses the metadata itself, in pure Go — no exiftool, so
  a scan works on a bare system. EXIF (`DateTimeOriginal`, then
  `DateTimeDigitized`, then IFD0 `DateTime`) for JPEG and every TIFF-based RAW
  (NEF, CR2, ARW, DNG, ORF, RW2, PEF), the wrapped JPEG's EXIF for Fujifilm RAF,
  and `moov`/`mvhd` for MP4/MOV. HEIC/AVIF are not parsed and fall back to the
  modtime. An all-zero date (camera clock never set) counts as absent.
- EXIF timestamps carry no zone — they are the camera's local wall clock, so
  they are read as local time. QuickTime times are UTC and converted.
- `scan.WalkSource()` = `Walk` + `FillCaptureTimes` (bounded worker pool).
  **Only source scans use it**; destinations are compared by relative path, so
  reading headers for thousands of files over a NAS share would cost time for
  nothing.
- `SplitStable` deliberately keeps using `ModTime` — a file written to the card
  seconds ago is still being written however old the shot inside it is. The
  destination copy is also still stamped with the source modtime.
- Backups made before this existed sit under their modtime's date. Nothing
  re-copies them: `MissingFromDest` also probes basename+size across the whole
  destination tree. `verify` needs the same fallback and gets it from `findCopy`
  (`scan.IndexByNameSize`) — without it every previously backed-up file would be
  reported missing.

### Is this destination file the same photograph?

Basename+size is a weak identity for the cross-date probe above: three cards all
number their first frame `DSC_0001`, and two of those frames can share a
byte-exact size. Trusting the match alone silently skipped a photo that was
never backed up — `status` said "0 missing" while `verify` reported a hash
mismatch on the same file.

Both paths therefore confirm a cross-date basename+size match against the
**capture time** of the destination file, and both must keep making the same
decision — a source `copy` skips must be a source `verify` considers present:

- `MissingFromDest` is two-pass: pass 1 settles everything decidable from the
  index alone and collects the ambiguous sources; pass 2 reads only those twins'
  capture times (`captureTimesOf`, bounded pool, deduplicated by path). A
  destination whose layout already matches costs **zero** reads. The split loses
  the input ordering, so `sortBySrcOrder` restores it — `SortBySizeAsc` and the
  videos-first sort are applied downstream and assume scan order.
- `verify.findCopy` does the same check inline; it is already hashing every file,
  so one header read is noise. Without it a different photograph would be hashed
  and reported as a *mismatch* when the truth is "never copied".
- Different capture times → different photographs → the source is copied. Equal,
  or no capture time on either side → treated as the same file, as before. This
  only ever skips *less* than the old rule, never more.
- Benchmarks live in `missing_bench_test.go` (3000 files): steady state ~4.7 ms
  with no destination reads, full migration ~20.5 ms, `WalkSource` ~41.9 ms vs
  `Walk` ~12.7 ms.

The residual gap is two frames shot on the **same day** with the same basename
and a byte-exact size — the exact-date-path match accepts those without reading
metadata. `verify` is what catches it: it hashes, so it reports the mismatch.

### What verify does and does not cover

`verify` is the backstop for everything the fast comparison approximates, so it
must never overstate what it checked:

- It hashes the **whole** file (`checksum.File`), on every side that is
  available. Nothing is sampled.
- It is **source-driven**: the authority is the camera/source, or the SSD when no
  source is mounted. Destination files with no counterpart on the authority are
  not examined — verifying a card checks that card's files, not the whole NAS.
- An **unmounted destination is skipped, not failed** — the other destinations
  are still worth checking. `verifyAll` therefore returns the configured roots it
  could not compare against (`unmountedRoots`), and both the CLI and the TUI
  print them beside the result. Without that, "All N files verified OK" stood for
  a destination nothing had looked at. A root whose *parent* exists counts as
  available (it is created on first copy), so the more common "share not mounted
  under an existing mount point" case fails loudly instead: every file reports
  `missing from NAS`.
- Only extensions in `file_extensions` are seen, by `verify` exactly as by
  `status` and `copy`. A file type left out of the list is invisible everywhere.

`TestCopyAndVerifyAgree` (verify package) is what keeps the two from drifting:
for each scenario it asserts *both* that `scan.MissingFromDest` skips the source
and that `verify` does not report it missing. Removing the capture-time check
from either side fails it, and the message says which side moved.

### Testing without camera files

The RAW samples that prove the preview chain are megabytes each and live outside
the repo: point `CAMERA_BACKUP_RAW_SAMPLES` at a directory of them and
`TestThumbnailNEF` runs, otherwise it skips. Nothing else needs them —
`firstDecodable` takes a tag→bytes getter, so the fallback order and the
skip-an-empty-tag behaviour (the actual NEF bug) are tested with synthesised
JPEG/TIFF payloads and no exiftool. Capture-time parsing likewise builds its own
JPEG, TIFF, RAF and MP4 fixtures (`buildJPEG`, `buildTIFF`, `buildRAF`,
`buildMOV` in `capture_test.go`).

Comparison uses filename + size (not hash) for speed. Collision: same name but different size is treated as a new file and saved with a `_N` suffix — the source is never modified. On re-runs, `MissingFromDest` also probes the `_N` variants by size so collision files are not copied again. It also probes basename+size **anywhere** in the destination tree — an old backup, or a source-modtime change on a file with no capture metadata, must not duplicate the file under a second date directory — but only skips on that evidence once the capture times agree; see "Is this destination file the same photograph?" above.

Source files whose modtime is within `scan.StableAge` (10 s) of the scan are treated as still being written and skipped with a warning (`scan.SplitStable`, applied in `runCopy`, `runDirect` and `status.Compute` → `StatusResult.CameraUnstable`); copying mid-write would produce a truncated destination. Far-future modtimes (wrong camera clock) are treated as stable.

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
- `h`/`l` (and `←`/`→`) walk the date tree: `enterNode` opens a group or steps to its first child (a file opens the preview), `leaveNode` closes an open group where it stands, and otherwise moves to the parent and closes that — `h` on a file must land on its day, not merely collapse in place. `parentIndex` finds the parent by scanning the flattened list upwards for the nearest lower level
- `[`/`]` jump between level-2 (date) rows; `z`/`Z` fold the whole tree shut/open and `f` folds everything but the current date. All three go through `relocate`, which puts the cursor back on the row it was on — or its nearest still-visible ancestor, so a fold never dumps the user at row 0
- Selection (`space`/`a`) filters what `y` copies; empty selection = copy everything. `y` also works from the grid view
- Grid view scrolls: `gridOffset` + `gridScrollToCursor()` keep the cursor row visible; thumbnails load incrementally for the visible window plus one lookahead row (no fixed cap)
- `?` opens the help screen (from main and grid); the Files panel title shows the visible row range (`Files 28–53/68`) when the tree overflows
- The help screen drops the blank lines between sections when they do not fit and scrolls with `j`/`k` beyond that (`helpBody`/`helpWindow`) — it has no room to lose a keybinding silently, and it outgrew 40 rows once already
- Worker counts come from `ssd_workers`/`nas_workers` in config.toml (defaults 3/1); NAS copies also honour `nas_write_timeout_seconds` and `nas_sync_order`. The write timeout is per-destination, not per-mode: pass it for any copy landing on the NAS (including verified direct dumps) and 0 for local Camera→SSD copies
- All copy batches are launched through `Model.launchBatch` (progress reset, screen switch, worker pool + drain wiring) — add new modes there rather than duplicating the plumbing
- Kitty graphics used for full-screen preview when `KittySupported()` (Ghostty/Kitty); block-art fallback otherwise; RAW previews need exiftool in PATH
- `KittySupported()` answers **no inside tmux** ($TMUX set): tmux swallows the graphics escapes unless allow-passthrough is on, and a swallowed image is an empty panel with nothing to explain it. `list_preview = "kitty"` forces the protocol back on for that case
- The Info panel and the grid draw real images over their own cells when `kittyList()`: the renderers reserve blank rows and `syncInfoPreview` / `syncGridPreview` draw into `infoPreviewRect` / `gridPlacements`. Both are funnelled through the call that already runs on every move (`maybeLoadThumb`, `loadThumbsForGridWindow`), and `kittyShown` holds a signature of what is on screen — a held-down `j` must not clear and re-send images per row, and a cursor move inside the grid window changes only labels, which are text. `kittySync` dispatches per screen and is called from the `tea.KeyMsg` branch of `Update` whenever the screen changes, so every transition either redraws or clears. `kittyDrawGridCmd` clears **once** before placing a screenful — clearing per image would wipe the ones already placed
- `gridPlacements` and `renderGrid` must agree on which cells are drawn as images, the same way `fileDetailLines` ties the Info panel's rows together
- `fileDetailLines` is the contract between `fileMetaLines` and `infoPreviewRect` — if they disagree the image lands on top of the file's details (`TestFileMetaLines_MatchesConstant`)
- `detailWidth()` widens the Info panel with the terminal (34/44/54 columns): block art spends one pixel per column, so panel width *is* preview resolution
- RAW previews come from whichever embedded image a file actually has. **Nikon NEF has no `ThumbnailImage`** — asking exiftool for that tag alone returns zero bytes, which is why NEF thumbnails were blank in both the list and the grid. `preview.Thumbnail` tries `PreviewImage` → `ThumbnailImage` → `JpgFromRaw` → `ThumbnailTIFF` (small first, so one exiftool call suffices for NEF/CR2/ARW); `FullImage` tries the same set largest-first. `ThumbnailTIFF` is why `golang.org/x/image/tiff` is registered as a decoder
- A failed thumbnail load is cached as nil like an unsupported one — without an entry the view would re-request it on every render. `preview.ErrNoRAWTool` sets `Model.rawToolMissing`, so the empty panel says to install exiftool instead of just `[no preview]`
- The date tree, the detail panel and the preview footer all show `DateTaken()`, so what the TUI groups by matches where `copy` will actually put the file; a fallback to the modtime is marked `(file date)`

### Device screen

`d` on the main screen opens `screenDevices` (`internal/tui/devices.go` for the
state and actions, `renderDevices` in view.go for the rendering). The same
screen has two jobs, told apart by `pickerMode`:

- `pickerSwap` (from the main screen): `enter` calls `useDevice`, which sets the
  source override, clears the selection (it pointed at the previous device's
  files), restarts the watcher and issues `statusScanCmd` — that rescan is what
  compares the new card against the NAS and, when `SSDInUse()`, the SSD. `s`
  additionally writes the device to config.toml as `source`, moving the previous
  `source` into `extra_sources` rather than dropping it.
- `pickerField` (from the settings screen's `d`, on the source fields only):
  `enter` fills that field via `applyPickedPath` and returns to the form, where
  the value is saved like any typed one. Destination roots are excluded — a NAS
  or SSD root is a fixed mount point that outlives any one card.

`statusReadyMsg` and `deviceChangedMsg` are accepted on `screenDevices` (as on
`screenSettings`) and must not switch the screen: the picker stays put after a
save, and a device event refreshes the list — plugging a card in while the
picker is open is exactly the case it exists for.

### Settings screen

`c` on the main screen opens `screenSettings` — an editable view of config.toml
(`internal/tui/settings.go` for the form, `renderSettings` in view.go for the
rendering). `tui.New` takes the config path for it; `""` makes it read-only.

- `settingsForm` holds an editable copy as strings, one `settingsField` per TOML
  key. `toConfig()` parses them onto a copy of the running config and returns the
  draft. Field-level input rules (empty required field, non-numeric count, empty
  `file_extensions`) live in `toConfig`; every cross-key rule comes from
  `config.Validate()` — do not duplicate those.
- Optional keys are seeded from their *effective* accessors (`SSDWorkerCount()`,
  `NASWriteTimeout()`, `SyncOrder()`), so the screen never shows a blank for a
  setting that is in force — and a save then writes those values explicitly.
- Path fields render a live `✔/✘` from `markerFor(value)`, which is passed the
  *in-progress editor text* rather than the field value, so feedback tracks
  keystrokes. Source paths must exist; destination roots use
  `config.RootAvailable` (parent counts).
- `config.Save()` is a line-oriented rewrite, not a re-encode: it replaces each
  key where it already appears (uncommenting it, keeping trailing comments) and
  appends only what is missing, writing atomically via a temp file + rename.
  `findAssignment` deliberately rewrites exactly one line and prefers a live
  assignment over a commented one, so prose that looks like `# key = value` is
  never turned into config.
- After a successful save `saveSettings()` swaps `m.cfg`, calls
  `restartWatcher()` (the watcher takes a stop channel so it can follow new
  paths) and issues a fresh `statusScanCmd` — edited paths take effect without a
  restart. `statusReadyMsg`/`deviceChangedMsg` are therefore accepted on
  `screenSettings`, and the handler must not switch the screen to `screenMain`
  there.
- `lineEditor` is a hand-rolled single-line editor (rune buffer + cursor, with a
  scrolling render window). Deliberate: no `bubbles` dependency, matching how
  this package already draws its own panels and progress bars.

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
- For the settings screen, copy testdata/config.toml somewhere writable and run
  against the copy — `s` rewrites the file in place. Worth checking after a save:
  the header/tabs/hints reflect the new config, the file kept its comments, and
  an invalid edit leaves the file byte-identical

### Platform-specific files

Linux is the only supported target. `internal/ui/freespace_unix.go` implements `FreeSpace()`; `freespace_windows.go` remains in the tree (build-tagged, never compiled on Linux) in case Windows support is ever revived.
