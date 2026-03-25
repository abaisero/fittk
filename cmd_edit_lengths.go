package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"github.com/muktihari/fit/proto"
	"github.com/spf13/cobra"
)

func editLengthsCmd() *cobra.Command {
	var setIdleIndices []int
	var setStrokeArgs []string
	var mergeArgs []string
	var outputPath string
	var silent bool

	cmd := &cobra.Command{
		Use:   "edit-lengths [--set-idle <indices>] [--set-stroke <index>:<stroke> ...] | [--merge <i>,<j> ...] [--output <output.fit>] [--silent] <input.fit>",
		Short: "Edit length properties and update all aggregates",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inputPath := args[0]
			if outputPath == inputPath {
				return fmt.Errorf("--output must be different from input")
			}

			mergeMode := cmd.Flags().Changed("merge")
			editMode := cmd.Flags().Changed("set-idle") || cmd.Flags().Changed("set-stroke")
			if mergeMode && editMode {
				return fmt.Errorf("--merge is mutually exclusive with --set-idle and --set-stroke")
			}
			if !mergeMode && !editMode {
				return fmt.Errorf("at least one of --set-idle, --set-stroke, or --merge is required")
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

			if mergeMode {
				return runMergeLengths(inputPath, outputPath, silent, mergeArgs, fit, oldSession, oldActivity, oldLaps, oldLengths, oldRecords)
			}

			// --- set-idle / set-stroke path ---

			// Parse --set-stroke args
			setStroke := make(map[int]typedef.SwimStroke)
			for _, s := range setStrokeArgs {
				parts := strings.SplitN(s, ":", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid --set-stroke format %q (expected index:stroke)", s)
				}
				idx, err := strconv.Atoi(parts[0])
				if err != nil {
					return fmt.Errorf("invalid index in --set-stroke %q: %w", s, err)
				}
				stroke, err := parseSwimStroke(parts[1])
				if err != nil {
					return fmt.Errorf("invalid stroke in --set-stroke %q: %w", s, err)
				}
				setStroke[idx] = stroke
			}

			// Validate all indices
			setIdleSet := make(map[int]bool)
			for _, i := range setIdleIndices {
				if i < 0 || i >= len(oldLengths) {
					return fmt.Errorf("length index %d out of range (0-%d)", i, len(oldLengths)-1)
				}
				setIdleSet[i] = true
			}
			for idx := range setStroke {
				if idx < 0 || idx >= len(oldLengths) {
					return fmt.Errorf("length index %d out of range (0-%d)", idx, len(oldLengths)-1)
				}
			}

			// Build modified length copies
			newLengths := make([]*mesgdef.Length, len(oldLengths))
			for i, l := range oldLengths {
				mesg := l.ToMesg(nil)
				newLengths[i] = mesgdef.NewLength(&mesg)
			}
			for i := range setIdleSet {
				setLengthIdle(newLengths[i])
			}
			for i, stroke := range setStroke {
				newLengths[i].SwimStroke = stroke
				newLengths[i].LengthType = typedef.LengthTypeActive
			}

			// Map each length index to its lap index
			lengthToLap := make(map[int]int)
			for lapIdx, lap := range oldLaps {
				first := int(lap.FirstLengthIndex)
				last := min(first+int(lap.NumLengths), len(oldLengths))
				for j := first; j < last; j++ {
					lengthToLap[j] = lapIdx
				}
			}

			// Determine which laps are affected
			affectedLaps := make(map[int]bool)
			for i := range setIdleSet {
				affectedLaps[lengthToLap[i]] = true
			}
			for i := range setStroke {
				affectedLaps[lengthToLap[i]] = true
			}

			// Recompute affected laps
			newLapByOldIdx := make(map[int]*mesgdef.Lap)
			for lapIdx, lap := range oldLaps {
				if !affectedLaps[lapIdx] {
					newLapByOldIdx[lapIdx] = lap
					continue
				}
				first := int(lap.FirstLengthIndex)
				last := min(first+int(lap.NumLengths), len(oldLengths))
				lapRecords := recordsForLap(lap, oldRecords)
				newLapByOldIdx[lapIdx] = recomputeLapStats(lap, oldLengths[first:last], lapRecords, newLengths[first:last], lapRecords)
			}

			var newLaps []*mesgdef.Lap
			for lapIdx := range oldLaps {
				newLaps = append(newLaps, newLapByOldIdx[lapIdx])
			}

			newSession := recomputeSessionStats(oldSession, oldLaps, oldLengths, oldRecords, newLaps, newLengths, oldRecords)

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

				for lapIdx, oldLap := range oldLaps {
					if !affectedLaps[lapIdx] {
						continue
					}
					first := int(oldLap.FirstLengthIndex)
					last := min(first+int(oldLap.NumLengths), len(oldLengths))

					fmt.Printf("=== lap #%d ===\n", lapIdx)
					compareMesgs(oldLap.ToMesg(nil), newLapByOldIdx[lapIdx].ToMesg(nil), "original", "updated")
					fmt.Println()

					for j := first; j < last; j++ {
						if !setIdleSet[j] && setStroke[j] == 0 {
							continue
						}
						fmt.Printf("  --- length #%d ---\n", j)
						compareMesgs(oldLengths[j].ToMesg(nil), newLengths[j].ToMesg(nil), "original", "updated")
						fmt.Println()
					}
				}
			}

			if outputPath == "" {
				return nil
			}

			lengthIdx := 0
			lapIdx := 0
			var out []proto.Message
			for _, mesg := range fit.Messages {
				switch mesg.Num {
				case mesgnum.Length:
					out = append(out, newLengths[lengthIdx].ToMesg(nil))
					lengthIdx++
				case mesgnum.Lap:
					out = append(out, newLapByOldIdx[lapIdx].ToMesg(nil))
					lapIdx++
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

	cmd.Flags().IntSliceVar(&setIdleIndices, "set-idle", nil, "length indices to reclassify as idle")
	cmd.Flags().StringArrayVar(&setStrokeArgs, "set-stroke", nil, "set stroke type: index:stroke (repeatable)")
	cmd.Flags().StringArrayVar(&mergeArgs, "merge", nil, "merge adjacent lengths: i,j (repeatable, mutually exclusive with --set-idle/--set-stroke)")
	cmd.Flags().StringVar(&outputPath, "output", "", "output file path")
	cmd.Flags().BoolVar(&silent, "silent", false, "suppress comparison output")
	return cmd
}

func runMergeLengths(
	inputPath, outputPath string,
	silent bool,
	mergeArgs []string,
	fit *proto.FIT,
	oldSession *mesgdef.Session,
	oldActivity *mesgdef.Activity,
	oldLaps []*mesgdef.Lap,
	oldLengths []*mesgdef.Length,
	oldRecords []*mesgdef.Record,
) error {
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

	// Validate all pairs before doing anything
	seenPairs := make(map[[2]int]bool)
	for _, p := range pairs {
		key := [2]int{p.i, p.j}
		if seenPairs[key] {
			return fmt.Errorf("duplicate pair %d,%d", p.i, p.j)
		}
		seenPairs[key] = true
		if p.j != p.i+1 {
			return fmt.Errorf("lengths %d and %d are not adjacent", p.i, p.j)
		}
		if p.i < 0 || p.j >= len(oldLengths) {
			return fmt.Errorf("merge index %d out of range (valid: 0-%d)", p.i, len(oldLengths)-2)
		}
		a, b := oldLengths[p.i], oldLengths[p.j]
		lapOfA, lapOfB := -1, -1
		for lapIdx, lap := range oldLaps {
			first := int(lap.FirstLengthIndex)
			last := first + int(lap.NumLengths)
			if p.i >= first && p.i < last {
				lapOfA = lapIdx
			}
			if p.j >= first && p.j < last {
				lapOfB = lapIdx
			}
		}
		if lapOfA != lapOfB || lapOfA < 0 {
			return fmt.Errorf("lengths %d and %d must belong to the same lap", p.i, p.j)
		}
		if a.LengthType != b.LengthType {
			return fmt.Errorf("lengths %d and %d have different types (%v vs %v)", p.i, p.j, a.LengthType, b.LengthType)
		}
		if a.LengthType == typedef.LengthTypeActive && a.SwimStroke != b.SwimStroke {
			return fmt.Errorf("lengths %d and %d have different strokes (%v vs %v)", p.i, p.j, a.SwimStroke, b.SwimStroke)
		}
	}

	// Sort descending so high-index merges don't affect lower indices
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].i > pairs[b].i })

	mergedByIdx := make(map[int]*mesgdef.Length)
	removedIdx := make(map[int]bool)
	for _, p := range pairs {
		mergedByIdx[p.i] = mergeTwoLengths(oldLengths[p.i], oldLengths[p.j])
		removedIdx[p.j] = true
	}

	var newLengths []*mesgdef.Length
	for k, l := range oldLengths {
		if removedIdx[k] {
			continue
		}
		if m, ok := mergedByIdx[k]; ok {
			newLengths = append(newLengths, m)
		} else {
			newLengths = append(newLengths, l)
		}
	}

	newLaps := make([]*mesgdef.Lap, len(oldLaps))
	for lapIdx, lap := range oldLaps {
		first := int(lap.FirstLengthIndex)
		last := first + int(lap.NumLengths)
		if last > len(oldLengths) {
			last = len(oldLengths)
		}
		shiftBefore, removedInLap := 0, 0
		for idx := range removedIdx {
			if idx < first {
				shiftBefore++
			} else if idx < last {
				removedInLap++
			}
		}
		switch {
		case removedInLap > 0:
			newFirst := first - shiftBefore
			lapNewLengths := newLengths[newFirst : newFirst+int(lap.NumLengths)-removedInLap]
			lapRecords := recordsForLap(lap, oldRecords)
			newLaps[lapIdx] = recomputeLapStats(lap, oldLengths[first:last], lapRecords, lapNewLengths, lapRecords)
		case shiftBefore > 0:
			mesg := lap.ToMesg(nil)
			shifted := mesgdef.NewLap(&mesg)
			shifted.FirstLengthIndex = uint16(first - shiftBefore)
			newLaps[lapIdx] = shifted
		default:
			newLaps[lapIdx] = lap
		}
	}

	newSession := recomputeSessionStats(oldSession, oldLaps, oldLengths, oldRecords, newLaps, newLengths, oldRecords)

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

		for lapIdx, oldLap := range oldLaps {
			first := int(oldLap.FirstLengthIndex)
			last := first + int(oldLap.NumLengths)
			if last > len(oldLengths) {
				last = len(oldLengths)
			}
			hasChange := false
			for k := first; k < last; k++ {
				if mergedByIdx[k] != nil || removedIdx[k] {
					hasChange = true
					break
				}
			}
			if !hasChange {
				continue
			}
			fmt.Printf("=== lap #%d ===\n", lapIdx)
			compareMesgs(oldLap.ToMesg(nil), newLaps[lapIdx].ToMesg(nil), "original", "updated")
			fmt.Println()
			for k := first; k < last; k++ {
				switch {
				case mergedByIdx[k] != nil:
					fmt.Printf("  --- length #%d (merged) ---\n", k)
					compareMesgs(oldLengths[k].ToMesg(nil), mergedByIdx[k].ToMesg(nil), "original", "updated")
				case removedIdx[k]:
					fmt.Printf("  --- length #%d (removed) ---\n", k)
					compareMesgs(oldLengths[k].ToMesg(nil), proto.Message{}, "original", "updated")
				default:
					fmt.Printf("  --- length #%d ---\n", k)
					compareMesgs(oldLengths[k].ToMesg(nil), oldLengths[k].ToMesg(nil), "original", "updated")
				}
				fmt.Println()
			}
		}
	}

	if outputPath == "" {
		return nil
	}

	var out []proto.Message
	lapIdx := 0
	lengthIdx := 0
	for _, mesg := range fit.Messages {
		switch mesg.Num {
		case mesgnum.Lap:
			out = append(out, newLaps[lapIdx].ToMesg(nil))
			lapIdx++
		case mesgnum.Length:
			if removedIdx[lengthIdx] {
				lengthIdx++
				continue
			}
			if m, ok := mergedByIdx[lengthIdx]; ok {
				out = append(out, m.ToMesg(nil))
			} else {
				out = append(out, mesg)
			}
			lengthIdx++
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
}

func writeFIT(fit *proto.FIT, outputPath string) error {
	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := encoder.New(f).Encode(fit); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	fmt.Printf("Done. Saved to %s\n", outputPath)
	return nil
}

func parseSwimStroke(s string) (typedef.SwimStroke, error) {
	switch strings.ToLower(s) {
	case "freestyle":
		return typedef.SwimStrokeFreestyle, nil
	case "backstroke":
		return typedef.SwimStrokeBackstroke, nil
	case "breaststroke":
		return typedef.SwimStrokeBreaststroke, nil
	case "butterfly":
		return typedef.SwimStrokeButterfly, nil
	case "drill":
		return typedef.SwimStrokeDrill, nil
	case "mixed":
		return typedef.SwimStrokeMixed, nil
	default:
		return 0, fmt.Errorf("unknown stroke %q (valid: freestyle, backstroke, breaststroke, butterfly, drill, mixed)", s)
	}
}

func setLengthIdle(l *mesgdef.Length) {
	l.LengthType = typedef.LengthTypeIdle
	l.SwimStroke = typedef.SwimStrokeInvalid
	l.TotalStrokes = basetype.Uint16Invalid
	l.AvgSpeed = basetype.Uint16Invalid
	l.AvgSwimmingCadence = basetype.Uint8Invalid
}

func mergeTwoLengths(a, b *mesgdef.Length) *mesgdef.Length {
	mesg := a.ToMesg(nil)
	m := mesgdef.NewLength(&mesg)

	m.TotalElapsedTime = a.TotalElapsedTime + b.TotalElapsedTime
	m.TotalTimerTime = a.TotalTimerTime + b.TotalTimerTime

	if a.LengthType == typedef.LengthTypeActive {
		if a.TotalStrokes != basetype.Uint16Invalid && b.TotalStrokes != basetype.Uint16Invalid {
			m.TotalStrokes = a.TotalStrokes + b.TotalStrokes
		}
		if m.TotalTimerTime > 0 {
			if a.AvgSpeed != basetype.Uint16Invalid && b.AvgSpeed != basetype.Uint16Invalid {
				m.AvgSpeed = uint16((uint64(a.AvgSpeed)*uint64(a.TotalTimerTime) + uint64(b.AvgSpeed)*uint64(b.TotalTimerTime)) / uint64(m.TotalTimerTime))
			}
			if m.TotalStrokes != basetype.Uint16Invalid {
				m.AvgSwimmingCadence = uint8(uint64(m.TotalStrokes) * 60000 / uint64(m.TotalTimerTime))
			}
		}
	}

	return m
}
