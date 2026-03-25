package main

import (
	"fmt"

	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"github.com/muktihari/fit/proto"

	"github.com/spf13/cobra"
)

func compareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compare <a.fit> <b.fit>",
		Short: "Compare session and laps between two FIT files",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			fitA, err := decodeFIT(args[0])
			if err != nil {
				return err
			}
			fitB, err := decodeFIT(args[1])
			if err != nil {
				return err
			}

			var sessionA, sessionB *mesgdef.Session
			var lapsA, lapsB []*mesgdef.Lap
			var lengthsA, lengthsB []*mesgdef.Length

			for i := range fitA.Messages {
				mesg := &fitA.Messages[i]
				switch mesg.Num {
				case mesgnum.Session:
					sessionA = mesgdef.NewSession(mesg)
				case mesgnum.Lap:
					lapsA = append(lapsA, mesgdef.NewLap(mesg))
				case mesgnum.Length:
					lengthsA = append(lengthsA, mesgdef.NewLength(mesg))
				}
			}
			for i := range fitB.Messages {
				mesg := &fitB.Messages[i]
				switch mesg.Num {
				case mesgnum.Session:
					sessionB = mesgdef.NewSession(mesg)
				case mesgnum.Lap:
					lapsB = append(lapsB, mesgdef.NewLap(mesg))
				case mesgnum.Length:
					lengthsB = append(lengthsB, mesgdef.NewLength(mesg))
				}
			}

			if sessionA == nil {
				return fmt.Errorf("%s: no session message found", args[0])
			}
			if sessionB == nil {
				return fmt.Errorf("%s: no session message found", args[1])
			}

			// Index laps in B by StartTime for matching
			lapBByStartTime := make(map[int64]*mesgdef.Lap)
			for _, lap := range lapsB {
				lapBByStartTime[lap.StartTime.Unix()] = lap
			}

			fileA, fileB := args[0], args[1]

			fmt.Println("=== session ===")
			compareMesgs(sessionA.ToMesg(nil), sessionB.ToMesg(nil), fileA, fileB)
			fmt.Println()

			lapAStartTimes := make(map[int64]bool)
			for _, lap := range lapsA {
				lapAStartTimes[lap.StartTime.Unix()] = true
			}

			for i, lapA := range lapsA {
				lapB, ok := lapBByStartTime[lapA.StartTime.Unix()]
				if !ok {
					fmt.Printf("=== lap #%d (only in %s) ===\n", i, fileA)
					compareMesgs(lapA.ToMesg(nil), proto.Message{}, fileA, fileB)
					fmt.Println()
					firstA := int(lapA.FirstLengthIndex)
					lastA := min(firstA+int(lapA.NumLengths), len(lengthsA))
					for j := firstA; j < lastA; j++ {
						fmt.Printf("  --- length #%d ---\n", j)
						compareMesgs(lengthsA[j].ToMesg(nil), proto.Message{}, fileA, fileB)
						fmt.Println()
					}
					continue
				}
				fmt.Printf("=== lap #%d ===\n", i)
				compareMesgs(lapA.ToMesg(nil), lapB.ToMesg(nil), fileA, fileB)
				fmt.Println()
				firstA := int(lapA.FirstLengthIndex)
				lastA := min(firstA+int(lapA.NumLengths), len(lengthsA))
				firstB := int(lapB.FirstLengthIndex)
				lastB := min(firstB+int(lapB.NumLengths), len(lengthsB))
				nLengths := max(lastA-firstA, lastB-firstB)
				for j := 0; j < nLengths; j++ {
					fmt.Printf("  --- length #%d ---\n", j)
					inA := firstA+j < lastA
					inB := firstB+j < lastB
					switch {
					case inA && inB:
						compareMesgs(lengthsA[firstA+j].ToMesg(nil), lengthsB[firstB+j].ToMesg(nil), fileA, fileB)
					case inA:
						compareMesgs(lengthsA[firstA+j].ToMesg(nil), proto.Message{}, fileA, fileB)
					case inB:
						compareMesgs(lengthsB[firstB+j].ToMesg(nil), proto.Message{}, fileB, fileA)
					}
					fmt.Println()
				}
			}

			// Laps in B not matched to any lap in A
			for i, lapB := range lapsB {
				if !lapAStartTimes[lapB.StartTime.Unix()] {
					fmt.Printf("=== lap #%d (only in %s) ===\n", i, fileB)
					compareMesgs(lapB.ToMesg(nil), proto.Message{}, fileB, fileA)
					fmt.Println()
					firstB := int(lapB.FirstLengthIndex)
					lastB := min(firstB+int(lapB.NumLengths), len(lengthsB))
					for j := firstB; j < lastB; j++ {
						fmt.Printf("  --- length #%d ---\n", j)
						compareMesgs(lengthsB[j].ToMesg(nil), proto.Message{}, fileB, fileA)
						fmt.Println()
					}
				}
			}

			return nil
		},
	}
}
