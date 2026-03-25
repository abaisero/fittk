package main

import (
	"fmt"
	"os"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"github.com/muktihari/fit/proto"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:           "fittk",
		Short:         "Inspect and clean Garmin swim FIT files",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(dumpCmd(), showLapsCmd(), dropLapsCmd(), editLengthsCmd(), verifySessionCmd(), verifyLapCmd(), compareCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func decodeFIT(path string) (*proto.FIT, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fit, err := decoder.New(f).Decode()
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return fit, nil
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	ms := int(d.Milliseconds()) % 1000
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d.%03d", h, m, s, ms)
	}
	return fmt.Sprintf("%d:%02d.%03d", m, s, ms)
}

func swimStrokeName(s typedef.SwimStroke) string {
	switch s {
	case typedef.SwimStrokeFreestyle:
		return "freestyle"
	case typedef.SwimStrokeBackstroke:
		return "backstroke"
	case typedef.SwimStrokeBreaststroke:
		return "breaststroke"
	case typedef.SwimStrokeButterfly:
		return "butterfly"
	case typedef.SwimStrokeDrill:
		return "drill"
	case typedef.SwimStrokeMixed:
		return "mixed"
	case typedef.SwimStrokeInvalid:
		return "(none)"
	default:
		return fmt.Sprintf("%d", s)
	}
}

func showLapsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show-laps <input.fit>",
		Short: "Display lap information",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fit, err := decodeFIT(args[0])
			if err != nil {
				return err
			}

			header := fmt.Sprintf("%3s  %8s  %8s  %7s  %7s  %-12s  %7s  %4s",
				"#", "Start", "Duration", "Dist(m)", "Lengths", "Stroke", "Strokes", "Cal")
			fmt.Println(header)
			fmt.Println(repeatStr("-", len(header)))

			i := 0
			for _, mesg := range fit.Messages {
				if mesg.Num != mesgnum.Lap {
					continue
				}
				lap := mesgdef.NewLap(&mesg)
				elapsed := time.Duration(lap.TotalElapsedTime) * time.Millisecond
				dist := float64(lap.TotalDistance) / 100.0
				fmt.Printf("%3d  %8s  %8s  %7.1f  %7d  %-12s  %7d  %4d\n",
					i,
					lap.StartTime.Local().Format("15:04:05"),
					formatDuration(elapsed),
					dist,
					lap.NumLengths,
					swimStrokeName(lap.SwimStroke),
					lap.TotalCycles,
					lap.TotalCalories,
				)
				i++
			}
			return nil
		},
	}
}


func ifValid[T uint8 | uint16 | uint32](oldValue, newValue T) T {
	if oldValue == ^T(0) {
		return oldValue
	}
	return newValue
}

// mesgTimestamp extracts the timestamp from a message's field number 253 (the
// FIT timestamp field), if present. Returns the zero time and false if absent.
func mesgTimestamp(mesg proto.Message) (time.Time, bool) {
	for _, f := range mesg.Fields {
		if f.Num == 253 {
			unix := int64(f.Value.Uint32())
			if unix == 0 {
				return time.Time{}, false
			}
			// FIT epoch is 1989-12-31 00:00:00 UTC
			const fitEpoch = 631065600
			return time.Unix(unix+fitEpoch, 0), true
		}
	}
	return time.Time{}, false
}

func repeatStr(s string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = s[0]
	}
	return string(b)
}
