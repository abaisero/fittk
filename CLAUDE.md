# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build

```
just build
```

## Architecture

This is a Go CLI tool for inspecting and editing Garmin swim FIT files. Single `main` package, flat layout at the root.

**Entry point**: `main.go` — registers cobra commands and contains shared utilities (`decodeFIT`, `formatDuration`, `swimStrokeName`, `mesgTimestamp`, `ifValid`).

**Commands**:
- `main.go` — `show-laps`: tabular lap summary
- `cmd_dump.go` — `dump`: prints all messages and fields with human-readable formatting; also contains `compareMesgs`
- `cmd_compare.go` — `compare`: side-by-side field diff of two FIT files (session, laps, lengths)
- `cmd_drop_laps.go` — `drop-laps`: drops trailing laps by index, removes all associated messages (records, events, device info), recomputes session/activity aggregates
- `cmd_edit_lengths.go` — `edit-lengths`: edits lengths via `--set-idle`/`--set-stroke`/`--set-stroke-count`, `--merge`, or `--split` (mutually exclusive groups); recomputes affected laps and session. `--set-stroke-count` sets a length's `total_strokes` (active lengths only) and recomputes its `avg_swimming_cadence`.
- `cmd_merge_laps.go` — `merge-laps`: merges adjacent laps via `--merge <i>,<j>` (repeatable, `j` must be `i+1`); the second lap's lengths become lengths of the first. No length messages are removed; the surviving lap expands to cover both and the absorbed lap's summary is dropped. Recomputes the merged lap and session.
- `lap.go` — `recomputeLapStats`, `recordsForLap`, `verify-laps` command
- `session.go` — `recomputeSessionStats`, `verify-session` command

**Key design decisions**:

- The library (`github.com/muktihari/fit`) is used at the `mesgdef` layer (typed structs) rather than raw `proto.Message`. Unknown/proprietary Garmin fields are preserved via `UnknownFields []proto.Field` on each mesgdef struct and appended back on `ToMesg()`, though they move to the end of the field list.
- `local_timestamp` in FIT encodes local wall clock time as FIT epoch seconds (not UTC). It must be displayed with `.UTC()`, not `.Local()`, unlike all other timestamp fields.
- FIT field values at the proto level are raw integers — scale and offset are not applied. `Field.Scale` and `Field.BaseType` are used in `cmd_dump.go` to format values correctly.
- Sentinel values (e.g. `0xFFFFFFFF` for uint32) indicate "not recorded" — checked via `Value.Valid(field.BaseType)` or compared against `basetype.Uint32Invalid` etc.
- `drop-laps` assumes dropped laps are always trailing. It uses a single `dropAfter` cutoff time (start of the earliest dropped lap) to remove all associated messages.
- `recomputeLapStats` preserves dead time (lap time not attributed to any length) when updating `TotalElapsedTime`/`TotalTimerTime`.
- `edit-lengths --merge` processes pairs high-to-low so earlier indices are unaffected by later merges. All pairs are validated before any changes are applied. Removing a length renumbers the remaining lengths' `MessageIndex` to stay contiguous, and laps with lengths removed *before* them (not just *within* them) have their `FirstLengthIndex` shifted accordingly — both kept consistent with `--split`, which renumbers on insert.
- `merge-laps` index handling: a rest lap reports `num_lengths=0` with `FirstLengthIndex` pointing at a boundary idle length, so the merged lap's `FirstLengthIndex` is taken from the first length it actually owns (the combined set), not blindly from the first lap. Calories are summed explicitly (`recomputeLapStats` does not recompute them, since they are unavailable at the length level). The merged lap's owned lengths must be contiguous in `MessageIndex` (errors otherwise). Surviving laps are renumbered into a contiguous `MessageIndex` range; `time_in_zone.reference_index` (matched by `reference_mesg == typedef.MesgNumLap`) and `session.first_lap_index` are repointed to the new indices, and the absorbed lap's `time_in_zone` is dropped.
- FIT addresses laps/lengths both by array position (file order) and by the `MessageIndex` field; in unedited Garmin files these coincide (contiguous `0..N`). Editing commands must preserve that invariant — keep `MessageIndex` contiguous and update every index reference — or downstream readers (and re-runs) mis-associate messages.
