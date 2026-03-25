# fittk

CLI toolkit for inspecting and correcting Garmin swim FIT files: fix mislabelled lengths, drop spurious laps, and recompute all aggregates.

> **Vibe-coded:** This project was built with AI assistance (Claude Code). There are no guarantees about correctness — use at your own risk. Always keep your original FIT files as backups.

## Motivation

Garmin's swim tracking is unreliable. Common problems:

- **Spurious laps** recorded when fumbling with the device at the end of a session.
- **Wrong stroke type** — Garmin misidentifies strokes, or you switched mid-length and it picked the wrong one.
- **Merged lengths** — two short lengths that should have been one (e.g., you paused mid-length).
- **Wrong active/idle classification** — a rest interval recorded as an active length or vice versa.

Existing tools either can't write FIT files at all, or silently drop Garmin's proprietary/unknown fields on write. `fittk` uses [`github.com/muktihari/fit`](https://github.com/muktihari/fit) at the mesgdef layer, which preserves unknown fields round-trip.

## Build

```
just build
```

Or directly:

```
go build -o build/fittk .
```

## Commands

### `show-laps`

Print a tabular summary of all laps.

```
fittk show-laps <input.fit>
```

### `dump`

Print all messages and fields with human-readable formatting (scale/offset applied, timestamps decoded).

```
fittk dump <input.fit>
```

### `compare`

Side-by-side field diff of two FIT files (session, laps, and lengths).

```
fittk compare <a.fit> <b.fit>
```

### `drop-laps`

Drop laps by index and recompute session/activity aggregates. Assumes dropped laps are trailing — all messages (lengths, records, events, device info) at or after the earliest dropped lap's start time are removed.

```
fittk drop-laps --laps 3,4 --output fixed.fit original.fit
```

Without `--output`, prints a diff of what would change without writing anything.

### `edit-lengths`

Edit per-length properties and recompute affected laps and the session. Two mutually exclusive modes:

**Set idle / set stroke** — reclassify lengths:

```
# Mark lengths 5 and 6 as idle (rest intervals)
fittk edit-lengths --set-idle 5,6 --output fixed.fit original.fit

# Change stroke type on a length
fittk edit-lengths --set-stroke 3:backstroke --output fixed.fit original.fit

# Both at once
fittk edit-lengths --set-idle 5 --set-stroke 3:backstroke --output fixed.fit original.fit
```

Valid stroke names: `freestyle`, `backstroke`, `breaststroke`, `butterfly`, `drill`, `mixed`.

**Merge** — combine two adjacent lengths into one (sums elapsed time, strokes, calories; takes stroke type from the first):

```
fittk edit-lengths --merge 2,3 --output fixed.fit original.fit

# Multiple merges at once
fittk edit-lengths --merge 2,3 --merge 7,8 --output fixed.fit original.fit
```

Both modes print a diff of all changed fields before writing. Use `--silent` to suppress.

### `verify-laps`

Compare all lap fields against values recomputed from lengths and records. Useful for checking whether Garmin's stored aggregates match what `fittk` would compute.

```
fittk verify-laps <input.fit>
```

### `verify-session`

Same as `verify-laps` but for the session message.

```
fittk verify-session <input.fit>
```

## Notes

- All editing commands require `--output` to actually write a file. Without it they just show the diff.
- `--output` must differ from the input path (no in-place editing).
- `drop-laps` assumes dropped laps are always trailing. Non-trailing lap removal is not supported.
- `edit-lengths --merge` requires both lengths to belong to the same lap and have the same type (and stroke, if active).
