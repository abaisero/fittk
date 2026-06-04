package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"github.com/muktihari/fit/proto"
	"github.com/spf13/cobra"
)

func mergeLapsCmd() *cobra.Command {
	var mergeArgs []string
	var outputPath string
	var silent bool

	cmd := &cobra.Command{
		Use:   "merge-laps --merge <i>,<j> [--merge ...] [--output <output.fit>] [--silent] <input.fit>",
		Short: "Merge adjacent laps, absorbing the second lap's lengths into the first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inputPath := args[0]
			if outputPath == inputPath {
				return fmt.Errorf("--output must be different from input")
			}
			if !cmd.Flags().Changed("merge") {
				return fmt.Errorf("--merge is required")
			}

			fit, err := decodeFIT(inputPath)
			if err != nil {
				return err
			}

			var oldSession *mesgdef.Session
			var oldActivity *mesgdef.Activity
			var oldLaps []*mesgdef.Lap
			var oldLengths []*mesgdef.Length
			var oldRecords []*mesgdef.Record

			for i := range fit.Messages {
				mesg := &fit.Messages[i]
				switch mesg.Num {
				case mesgnum.Session:
					oldSession = mesgdef.NewSession(mesg)
				case mesgnum.Activity:
					oldActivity = mesgdef.NewActivity(mesg)
				case mesgnum.Lap:
					oldLaps = append(oldLaps, mesgdef.NewLap(mesg))
				case mesgnum.Length:
					oldLengths = append(oldLengths, mesgdef.NewLength(mesg))
				case mesgnum.Record:
					oldRecords = append(oldRecords, mesgdef.NewRecord(mesg))
				}
			}

			if oldSession == nil {
				return fmt.Errorf("no session message found")
			}

			type pair struct{ i, j int }
			var pairs []pair
			for _, s := range mergeArgs {
				parts := strings.SplitN(s, ",", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid --merge %q (expected i,j)", s)
				}
				var p pair
				if _, err := fmt.Sscan(parts[0], &p.i); err != nil {
					return fmt.Errorf("invalid --merge %q: %w", s, err)
				}
				if _, err := fmt.Sscan(parts[1], &p.j); err != nil {
					return fmt.Errorf("invalid --merge %q: %w", s, err)
				}
				pairs = append(pairs, p)
			}

			// Validate all pairs before doing anything. Each lap may appear in at
			// most one merge, so chained requests (e.g. 0,1 and 1,2) are rejected;
			// merge incrementally instead.
			used := make(map[int]bool)
			for _, p := range pairs {
				if p.j != p.i+1 {
					return fmt.Errorf("laps %d and %d are not adjacent", p.i, p.j)
				}
				if p.i < 0 || p.j >= len(oldLaps) {
					return fmt.Errorf("merge index %d out of range (valid: 0-%d)", p.i, len(oldLaps)-2)
				}
				if used[p.i] || used[p.j] {
					return fmt.Errorf("lap %d or %d is referenced in more than one merge", p.i, p.j)
				}
				used[p.i] = true
				used[p.j] = true
			}

			// Sort descending for stable, deterministic processing. Each merge is
			// computed from the original data, so order does not affect results.
			sort.Slice(pairs, func(a, b int) bool { return pairs[a].i > pairs[b].i })

			mergedByIdx := make(map[int]*mesgdef.Lap)
			removedIdx := make(map[int]bool)
			for _, p := range pairs {
				merged, err := mergeTwoLaps(oldLaps[p.i], oldLaps[p.j], oldLengths, oldRecords)
				if err != nil {
					return fmt.Errorf("merge %d,%d: %w", p.i, p.j, err)
				}
				mergedByIdx[p.i] = merged
				removedIdx[p.j] = true
			}

			// Lengths are unchanged by a lap merge; only the Lap summary for the
			// absorbed lap is dropped and the surviving lap expands to cover both.
			// Surviving laps are renumbered into a contiguous message-index range;
			// oldToNewLapIdx maps each kept lap's old position to its new index so
			// references to it (time_in_zone, session.first_lap_index) can follow.
			var newLaps []*mesgdef.Lap
			oldToNewLapIdx := make(map[int]int)
			for k, lap := range oldLaps {
				if removedIdx[k] {
					continue
				}
				oldToNewLapIdx[k] = len(newLaps)
				if m, ok := mergedByIdx[k]; ok {
					newLaps = append(newLaps, m)
				} else {
					newLaps = append(newLaps, lap)
				}
			}

			newSession := recomputeSessionStats(oldSession, oldLaps, oldLengths, oldRecords, newLaps, oldLengths, oldRecords)
			if oldSession.FirstLapIndex != basetype.Uint16Invalid {
				if ni, ok := oldToNewLapIdx[int(oldSession.FirstLapIndex)]; ok {
					newSession.FirstLapIndex = uint16(ni)
				}
			}

			var newActivity *mesgdef.Activity
			if oldActivity != nil {
				mesg := oldActivity.ToMesg(nil)
				newActivity = mesgdef.NewActivity(&mesg)
				newActivity.TotalTimerTime = newSession.TotalTimerTime
			}

			if !silent {
				fmt.Println("=== session ===")
				compareMesgs(oldSession.ToMesg(nil), newSession.ToMesg(nil), "original", "updated")
				fmt.Println()

				for k, oldLap := range oldLaps {
					switch {
					case mergedByIdx[k] != nil:
						fmt.Printf("=== lap #%d (merged) ===\n", k)
						compareMesgs(oldLap.ToMesg(nil), mergedByIdx[k].ToMesg(nil), "original", "updated")
						fmt.Println()
					case removedIdx[k]:
						fmt.Printf("=== lap #%d (removed) ===\n", k)
						compareMesgs(oldLap.ToMesg(nil), proto.Message{}, "original", "updated")
						fmt.Println()
					}
				}
			}

			if outputPath == "" {
				return nil
			}

			var out []proto.Message
			lapIdx := 0
			for _, mesg := range fit.Messages {
				switch mesg.Num {
				case mesgnum.Lap:
					if removedIdx[lapIdx] {
						lapIdx++
						continue
					}
					// Renumber MessageIndex into the contiguous surviving range.
					newMsgIdx := typedef.MessageIndex(oldToNewLapIdx[lapIdx])
					if m, ok := mergedByIdx[lapIdx]; ok {
						m.MessageIndex = newMsgIdx
						out = append(out, m.ToMesg(nil))
					} else {
						l := mesgdef.NewLap(&mesg)
						l.MessageIndex = newMsgIdx
						out = append(out, l.ToMesg(nil))
					}
					lapIdx++
				case mesgnum.TimeInZone:
					// time-in-zone summaries reference a lap by its message index:
					// drop the absorbed lap's, repoint the rest at their new index.
					tiz := mesgdef.NewTimeInZone(&mesg)
					if tiz.ReferenceMesg == typedef.MesgNumLap {
						oldRef := int(tiz.ReferenceIndex)
						if removedIdx[oldRef] {
							continue
						}
						if ni, ok := oldToNewLapIdx[oldRef]; ok {
							tiz.ReferenceIndex = typedef.MessageIndex(ni)
							out = append(out, tiz.ToMesg(nil))
							continue
						}
					}
					out = append(out, mesg)
				case mesgnum.Session:
					out = append(out, newSession.ToMesg(nil))
				case mesgnum.Activity:
					if newActivity != nil {
						out = append(out, newActivity.ToMesg(nil))
					} else {
						out = append(out, mesg)
					}
				default:
					out = append(out, mesg)
				}
			}

			fit.Messages = out
			return writeFIT(fit, outputPath)
		},
	}

	cmd.Flags().StringArrayVar(&mergeArgs, "merge", nil, "merge adjacent laps: i,j (repeatable)")
	cmd.Flags().StringVar(&outputPath, "output", "", "output file path")
	cmd.Flags().BoolVar(&silent, "silent", false, "suppress comparison output")
	return cmd
}

// mergeTwoLaps merges lap b into lap a, treating b's lengths as additional
// lengths of a. The lengths themselves are unchanged; a's range simply extends
// to cover b's. Aggregates are recomputed from the combined lengths and records.
//
// A synthetic "old lap" carrying the summed elapsed/timer/distance/active-length
// totals is passed to recomputeLapStats so that dead time (lap time not
// attributed to any length) and the derived pool length are preserved across
// both laps.
//
// first_length_index is set from the first length the merged lap actually owns,
// not from a's: a rest lap reports num_lengths=0 with first_length_index
// pointing at a boundary idle length, so blindly keeping a's value would
// mis-associate the lengths.
func mergeTwoLaps(a, b *mesgdef.Lap, oldLengths []*mesgdef.Length, oldRecords []*mesgdef.Record) (*mesgdef.Lap, error) {
	aFirst := int(a.FirstLengthIndex)
	aLast := min(aFirst+int(a.NumLengths), len(oldLengths))
	bFirst := int(b.FirstLengthIndex)
	bLast := min(bFirst+int(b.NumLengths), len(oldLengths))

	combinedLengths := make([]*mesgdef.Length, 0, (aLast-aFirst)+(bLast-bFirst))
	combinedLengths = append(combinedLengths, oldLengths[aFirst:aLast]...)
	combinedLengths = append(combinedLengths, oldLengths[bFirst:bLast]...)

	// A lap's lengths are addressed by a single (first_length_index, num_lengths)
	// span, so the merged lap's owned lengths must form one contiguous block of
	// message indices. An orphan idle length recorded at the rest boundary
	// between the two laps is fine (it sits at the edge, outside both counted
	// ranges); a gap between two counted lengths is not representable.
	for k := 1; k < len(combinedLengths); k++ {
		if combinedLengths[k].MessageIndex != combinedLengths[k-1].MessageIndex+1 {
			return nil, fmt.Errorf("laps own non-contiguous lengths (indices %d and %d); cannot merge", combinedLengths[k-1].MessageIndex, combinedLengths[k].MessageIndex)
		}
	}

	combinedRecords := append(recordsForLap(a, oldRecords), recordsForLap(b, oldRecords)...)

	mesg := a.ToMesg(nil)
	merged := mesgdef.NewLap(&mesg)
	if len(combinedLengths) > 0 {
		merged.FirstLengthIndex = uint16(combinedLengths[0].MessageIndex)
	}
	if a.TotalElapsedTime != basetype.Uint32Invalid && b.TotalElapsedTime != basetype.Uint32Invalid {
		merged.TotalElapsedTime = a.TotalElapsedTime + b.TotalElapsedTime
	}
	if a.TotalTimerTime != basetype.Uint32Invalid && b.TotalTimerTime != basetype.Uint32Invalid {
		merged.TotalTimerTime = a.TotalTimerTime + b.TotalTimerTime
	}
	if a.TotalDistance != basetype.Uint32Invalid && b.TotalDistance != basetype.Uint32Invalid {
		merged.TotalDistance = a.TotalDistance + b.TotalDistance
	}
	if a.NumActiveLengths != basetype.Uint16Invalid && b.NumActiveLengths != basetype.Uint16Invalid {
		merged.NumActiveLengths = a.NumActiveLengths + b.NumActiveLengths
	}
	// Calories are not available at the length level, so recomputeLapStats leaves
	// them untouched; sum them here so the absorbed lap's calories are retained.
	if a.TotalCalories != basetype.Uint16Invalid && b.TotalCalories != basetype.Uint16Invalid {
		merged.TotalCalories = a.TotalCalories + b.TotalCalories
	}

	return recomputeLapStats(merged, combinedLengths, combinedRecords, combinedLengths, combinedRecords), nil
}
