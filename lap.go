package main

import (
	"fmt"
	"math"
	"time"

	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"github.com/muktihari/fit/proto"
	"github.com/spf13/cobra"
)

// recomputeLapStats returns a copy of oldLap with recomputable fields updated
// from the new messages. Fields that are invalid in oldLap are left invalid in
// the result.
//
// The old* slices represent the original full set of messages for this lap. The
// new* slices represent the messages that will remain after any edits.
// recordsForLap returns the subset of records whose timestamp falls within the
// lap's time range. The end time is derived from StartTime + TotalElapsedTime
// rather than Timestamp, since some Garmin files store an incorrect Timestamp
// on lap messages.
func recordsForLap(lap *mesgdef.Lap, records []*mesgdef.Record) []*mesgdef.Record {
	end := lap.StartTime.Add(time.Duration(float64(lap.TotalElapsedTime) / 1000 * float64(time.Second)))
	var result []*mesgdef.Record
	for _, rec := range records {
		if !rec.Timestamp.Before(lap.StartTime) && !rec.Timestamp.After(end) {
			result = append(result, rec)
		}
	}
	return result
}

func recomputeLapStats(
	oldLap *mesgdef.Lap,
	oldLengths []*mesgdef.Length,
	oldRecords []*mesgdef.Record,
	newLengths []*mesgdef.Length,
	newRecords []*mesgdef.Record,
) *mesgdef.Lap {
	mesg := oldLap.ToMesg(nil)
	l := mesgdef.NewLap(&mesg)

	// Timestamp: end time of last new length
	if !oldLap.Timestamp.IsZero() && len(newLengths) > 0 {
		l.Timestamp = newLengths[len(newLengths)-1].Timestamp
	}

	// Summable from new lengths (calories not available at length level)
	var totalElapsed, totalTimer, totalCycles uint32
	var numLengths, numActiveLengths uint16
	for _, length := range newLengths {
		totalElapsed += length.TotalElapsedTime
		totalTimer += length.TotalTimerTime
		numLengths++
		if length.LengthType == typedef.LengthTypeActive {
			totalCycles += uint32(length.TotalStrokes)
			numActiveLengths++
		}
	}

	// TotalElapsedTime and TotalTimerTime: preserve dead time (lap time not
	// attributed to any length).
	// dead_time = old_lap.Total*Time - sum(old_lengths.Total*Time)
	if oldLap.TotalElapsedTime != basetype.Uint32Invalid {
		var oldLengthElapsed uint32
		for _, length := range oldLengths {
			oldLengthElapsed += length.TotalElapsedTime
		}
		l.TotalElapsedTime = totalElapsed + (oldLap.TotalElapsedTime - oldLengthElapsed)
	}
	if oldLap.TotalTimerTime != basetype.Uint32Invalid {
		var oldLengthTimer uint32
		for _, length := range oldLengths {
			oldLengthTimer += length.TotalTimerTime
		}
		l.TotalTimerTime = totalTimer + (oldLap.TotalTimerTime - oldLengthTimer)
	}

	l.NumLengths = ifValid(oldLap.NumLengths, numLengths)
	l.NumActiveLengths = ifValid(oldLap.NumActiveLengths, numActiveLengths)
	l.TotalCycles = ifValid(oldLap.TotalCycles, totalCycles)


	// TotalDistance: num active lengths * pool length (derived from old lap)
	if oldLap.TotalDistance != basetype.Uint32Invalid && oldLap.NumActiveLengths > 0 {
		poolLength := oldLap.TotalDistance / uint32(oldLap.NumActiveLengths)
		l.TotalDistance = poolLength * uint32(numActiveLengths)
	}

	// MaxSpeed: max of new length AvgSpeed
	// AvgSpeed: TotalDistance / total active swim time (rest lengths excluded)
	var maxSpeed uint16
	var activeTimer uint32
	for _, length := range newLengths {
		if length.AvgSpeed != basetype.Uint16Invalid && length.AvgSpeed > maxSpeed {
			maxSpeed = length.AvgSpeed
		}
		if length.LengthType == typedef.LengthTypeActive {
			activeTimer += length.TotalTimerTime
		}
	}
	l.MaxSpeed = ifValid(oldLap.MaxSpeed, maxSpeed)
	l.EnhancedMaxSpeed = ifValid(oldLap.EnhancedMaxSpeed, uint32(maxSpeed))

	if activeTimer > 0 {
		avgSpeed := uint16(uint64(l.TotalDistance) * 10000 / uint64(activeTimer))
		l.AvgSpeed = ifValid(oldLap.AvgSpeed, avgSpeed)
		l.EnhancedAvgSpeed = ifValid(oldLap.EnhancedAvgSpeed, uint32(avgSpeed))
	}

	// HR from new records
	var hrSum, hrCount uint64
	var maxHR uint8
	for _, rec := range newRecords {
		if rec.HeartRate != basetype.Uint8Invalid {
			hrSum += uint64(rec.HeartRate)
			hrCount++
			if rec.HeartRate > maxHR {
				maxHR = rec.HeartRate
			}
		}
	}
	if hrCount > 0 {
		if oldLap.AvgHeartRate != basetype.Uint8Invalid {
			l.AvgHeartRate = uint8((hrSum + hrCount/2) / hrCount)
		}
		if oldLap.MaxHeartRate != basetype.Uint8Invalid {
			l.MaxHeartRate = maxHR
		}
	}

	// avg_swolf: Garmin proprietary field 73, not in the open FIT spec.
	// Formula: (total_timer_s + total_strokes) / num_active_lengths
	if numActiveLengths > 0 && totalCycles != basetype.Uint32Invalid {
		avgSwolf := uint16(math.Round(float64(totalTimer/1000+totalCycles) / float64(numActiveLengths)))
		for i, f := range l.UnknownFields {
			if f.Num == 73 {
				l.UnknownFields[i].Value = proto.Uint16(avgSwolf)
				break
			}
		}
	}

	return l
}

func verifyLapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify-laps <input.fit>",
		Short: "Compare all lap fields against recomputed values",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fit, err := decodeFIT(args[0])
			if err != nil {
				return err
			}

			var laps []*mesgdef.Lap
			var lengths []*mesgdef.Length
			var records []*mesgdef.Record

			for i := range fit.Messages {
				mesg := &fit.Messages[i]
				switch mesg.Num {
				case mesgnum.Lap:
					laps = append(laps, mesgdef.NewLap(mesg))
				case mesgnum.Length:
					lengths = append(lengths, mesgdef.NewLength(mesg))
				case mesgnum.Record:
					records = append(records, mesgdef.NewRecord(mesg))
				}
			}

			for i, lap := range laps {
				first := int(lap.FirstLengthIndex)
				last := first + int(lap.NumLengths)
				if last > len(lengths) {
					last = len(lengths)
				}
				lapLengths := lengths[first:last]
				lapRecords := recordsForLap(lap, records)

				fmt.Printf("=== lap #%d ===\n", i)
				oldMesg := lap.ToMesg(nil)
				recompMesg := recomputeLapStats(lap, lapLengths, lapRecords, lapLengths, lapRecords).ToMesg(nil)
				compareMesgs(oldMesg, recompMesg, "original", "recomputed")
				fmt.Println()
			}

			return nil
		},
	}
}
