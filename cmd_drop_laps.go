package main

import (
	"fmt"
	"os"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"github.com/muktihari/fit/proto"
	"github.com/spf13/cobra"
)

func dropLapsCmd() *cobra.Command {
	var lapIndices []int
	var outputPath string
	var silent bool

	cmd := &cobra.Command{
		Use:   "drop-laps [--laps <indices>] [--output <output.fit>] [--silent] <input.fit>",
		Short: "Drop laps and update all aggregates",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inputPath := args[0]
			if outputPath == inputPath {
				return fmt.Errorf("--output must be different from input")
			}

			fit, err := decodeFIT(inputPath)
			if err != nil {
				return err
			}

			// Collect all typed messages
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

			// Validate and build drop set
			droppedLaps := make(map[int]bool)
			for _, i := range lapIndices {
				if i < 0 || i >= len(oldLaps) {
					return fmt.Errorf("lap index %d out of range (0-%d)", i, len(oldLaps)-1)
				}
				droppedLaps[i] = true
			}

			// Dropped laps are assumed to be trailing. Everything at or after the
			// earliest dropped lap's start time is removed.
			var dropAfter time.Time
			for lapIdx, lap := range oldLaps {
				if droppedLaps[lapIdx] && (dropAfter.IsZero() || lap.StartTime.Before(dropAfter)) {
					dropAfter = lap.StartTime
				}
			}
			afterCutoff := func(t time.Time) bool {
				return !dropAfter.IsZero() && !t.Before(dropAfter)
			}

			// Kept laps are unchanged (no lengths within them are dropped).
			newLapByOldIdx := make(map[int]*mesgdef.Lap)
			for lapIdx, lap := range oldLaps {
				if !droppedLaps[lapIdx] {
					newLapByOldIdx[lapIdx] = lap
				}
			}

			var newLaps []*mesgdef.Lap
			for lapIdx := range oldLaps {
				if !droppedLaps[lapIdx] {
					newLaps = append(newLaps, newLapByOldIdx[lapIdx])
				}
			}

			var newLengths []*mesgdef.Length
			for _, length := range oldLengths {
				if !afterCutoff(length.StartTime) {
					newLengths = append(newLengths, length)
				}
			}

			newSession := recomputeSessionStats(oldSession, oldLaps, oldLengths, oldRecords, newLaps, newLengths, oldRecords)
			// Dead time from the dropped period is no longer valid; clamp to timer time.
			newSession.TotalElapsedTime = newSession.TotalTimerTime

			var newActivity *mesgdef.Activity
			if oldActivity != nil {
				mesg := oldActivity.ToMesg(nil)
				newActivity = mesgdef.NewActivity(&mesg)
				newActivity.TotalTimerTime = newSession.TotalTimerTime
			}

			// Print comparison unless silenced
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

					if droppedLaps[lapIdx] {
						fmt.Printf("=== lap #%d (dropped) ===\n", lapIdx)
						compareMesgs(oldLap.ToMesg(nil), proto.Message{}, "original", "updated")
						fmt.Println()
						for j := first; j < last; j++ {
							fmt.Printf("  --- length #%d (dropped) ---\n", j)
							compareMesgs(oldLengths[j].ToMesg(nil), proto.Message{}, "original", "updated")
							fmt.Println()
						}
						continue
					}

					fmt.Printf("=== lap #%d ===\n", lapIdx)
					compareMesgs(oldLap.ToMesg(nil), newLapByOldIdx[lapIdx].ToMesg(nil), "original", "updated")
					fmt.Println()
					for j := first; j < last; j++ {
						fmt.Printf("  --- length #%d ---\n", j)
						compareMesgs(oldLengths[j].ToMesg(nil), oldLengths[j].ToMesg(nil), "original", "updated")
						fmt.Println()
					}
				}
			}

			if outputPath == "" {
				return nil
			}

			// Rebuild message stream
			var out []proto.Message
			lapIdx := 0
			lastLapDropped := false

			for _, mesg := range fit.Messages {
				switch mesg.Num {
				case mesgnum.Lap:
					if droppedLaps[lapIdx] {
						lastLapDropped = true
						lapIdx++
						continue
					}
					out = append(out, newLapByOldIdx[lapIdx].ToMesg(nil))
					lastLapDropped = false
					lapIdx++

				case mesgnum.TimeInZone:
					if lastLapDropped {
						continue
					}
					out = append(out, mesg)

				case mesgnum.Length:
					length := mesgdef.NewLength(&mesg)
					if afterCutoff(length.StartTime) {
						continue
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
					if ts, ok := mesgTimestamp(mesg); ok && afterCutoff(ts) {
						continue
					}
					out = append(out, mesg)
				}
			}

			fit.Messages = out

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
		},
	}

	cmd.Flags().IntSliceVar(&lapIndices, "laps", nil, "lap indices to drop")
	cmd.Flags().StringVar(&outputPath, "output", "", "output file path")
	cmd.Flags().BoolVar(&silent, "silent", false, "suppress comparison output")
	return cmd
}
