package main

import (
	"fmt"
	"math"

	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"github.com/muktihari/fit/proto"
	"github.com/spf13/cobra"
)

// recomputeSessionStats returns a copy of oldSession with recomputable fields
// updated from the new messages. Fields that are invalid in oldSession are left
// invalid in the result.
//
// The old* slices represent the oldinal full set of messages (used to compute
// dead time). The new* slices represent the messages that will remain after any
// edits.
func recomputeSessionStats(
	oldSession *mesgdef.Session,
	oldLaps []*mesgdef.Lap,
	oldLengths []*mesgdef.Length,
	oldRecords []*mesgdef.Record,
	newLaps []*mesgdef.Lap,
	newLengths []*mesgdef.Length,
	newRecords []*mesgdef.Record,
) *mesgdef.Session {
	mesg := oldSession.ToMesg(nil)
	s := mesgdef.NewSession(&mesg)

	// Timestamp: end time of last new lap
	if !oldSession.Timestamp.IsZero() && len(newLaps) > 0 {
		s.Timestamp = newLaps[len(newLaps)-1].Timestamp
	}

	// Summable from new laps
	var totalElapsed, totalTimer, totalDistance, totalCycles uint32
	var totalCalories uint16
	var numLengths, numActiveLengths uint16
	for _, lap := range newLaps {
		totalElapsed += lap.TotalElapsedTime
		totalTimer += lap.TotalTimerTime
		totalDistance += lap.TotalDistance
		totalCycles += lap.TotalCycles
		totalCalories += lap.TotalCalories
		numLengths += lap.NumLengths
		numActiveLengths += lap.NumActiveLengths
	}

	// TotalElapsedTime: preserve dead time (session time not attributed to any lap)
	// dead_time = old_session.TotalElapsedTime - sum(old_laps.TotalElapsedTime)
	if oldSession.TotalElapsedTime != basetype.Uint32Invalid {
		var oldLapElapsed uint32
		for _, lap := range oldLaps {
			oldLapElapsed += lap.TotalElapsedTime
		}
		deadTime := oldSession.TotalElapsedTime - oldLapElapsed
		s.TotalElapsedTime = totalElapsed + deadTime
	}

	s.NumLaps = ifValid(oldSession.NumLaps, uint16(len(newLaps)))
	s.NumLengths = ifValid(oldSession.NumLengths, numLengths)
	s.NumActiveLengths = ifValid(oldSession.NumActiveLengths, numActiveLengths)
	s.TotalTimerTime = ifValid(oldSession.TotalTimerTime, totalTimer)
	s.TotalDistance = ifValid(oldSession.TotalDistance, totalDistance)
	s.TotalCycles = ifValid(oldSession.TotalCycles, totalCycles)
	s.TotalCalories = ifValid(oldSession.TotalCalories, totalCalories)

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
	s.MaxSpeed = ifValid(oldSession.MaxSpeed, maxSpeed)
	s.EnhancedMaxSpeed = ifValid(oldSession.EnhancedMaxSpeed, uint32(maxSpeed))

	if activeTimer > 0 {
		// AvgSpeed (m/s, scale 1000) = (TotalDistance/100) / (activeTimer/1000) * 1000
		// = TotalDistance * 10000 / activeTimer
		avgSpeed := uint16(uint64(totalDistance) * 10000 / uint64(activeTimer))
		s.AvgSpeed = ifValid(oldSession.AvgSpeed, avgSpeed)
		s.EnhancedAvgSpeed = ifValid(oldSession.EnhancedAvgSpeed, uint32(avgSpeed))
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
		if oldSession.AvgHeartRate != basetype.Uint8Invalid {
			s.AvgHeartRate = uint8((hrSum + hrCount/2) / hrCount)
		}
		if oldSession.MaxHeartRate != basetype.Uint8Invalid {
			s.MaxHeartRate = maxHR
		}
	}

	// avg_swolf: Garmin proprietary field 80, not in the open FIT spec.
	// Weighted average of lap avg_swolf (field 73) weighted by num_active_lengths.
	var swolfWeightedSum, swolfTotalActive uint32
	for _, lap := range newLaps {
		if lap.NumActiveLengths == 0 {
			continue
		}
		for _, f := range lap.UnknownFields {
			if f.Num == 73 {
				swolfWeightedSum += uint32(f.Value.Uint16()) * uint32(lap.NumActiveLengths)
				swolfTotalActive += uint32(lap.NumActiveLengths)
				break
			}
		}
	}
	if swolfTotalActive > 0 {
		avgSwolf := uint16(math.Round(float64(swolfWeightedSum) / float64(swolfTotalActive)))
		for i, f := range s.UnknownFields {
			if f.Num == 80 {
				s.UnknownFields[i].Value = proto.Uint16(avgSwolf)
				break
			}
		}
	}

	return s
}

func verifySessionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify-session <input.fit>",
		Short: "Compare session fields against recomputed values",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fit, err := decodeFIT(args[0])
			if err != nil {
				return err
			}

			var session *mesgdef.Session
			var laps []*mesgdef.Lap
			var lengths []*mesgdef.Length
			var records []*mesgdef.Record

			for i := range fit.Messages {
				mesg := &fit.Messages[i]
				switch mesg.Num {
				case mesgnum.Session:
					session = mesgdef.NewSession(mesg)
				case mesgnum.Lap:
					laps = append(laps, mesgdef.NewLap(mesg))
				case mesgnum.Length:
					lengths = append(lengths, mesgdef.NewLength(mesg))
				case mesgnum.Record:
					records = append(records, mesgdef.NewRecord(mesg))
				}
			}

			if session == nil {
				return fmt.Errorf("no session message found")
			}

			oldMesg := session.ToMesg(nil)
			recompMesg := recomputeSessionStats(session, laps, lengths, records, laps, lengths, records).ToMesg(nil)
			compareMesgs(oldMesg, recompMesg, "original", "recomputed")

			return nil
		},
	}
}
