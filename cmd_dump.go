package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/muktihari/fit/kit/datetime"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
	"github.com/spf13/cobra"
)

var timestampFieldNames = map[string]bool{
	"timestamp":       true,
	"time_created":    true,
	"local_timestamp": true,
	"start_time":      true,
}

func isTimestampField(name string) bool {
	return timestampFieldNames[name] || strings.HasSuffix(name, "_timestamp")
}

func numericValue(v proto.Value) float64 {
	switch v.Type() {
	case proto.TypeInt8:
		return float64(v.Int8())
	case proto.TypeUint8:
		return float64(v.Uint8())
	case proto.TypeInt16:
		return float64(v.Int16())
	case proto.TypeUint16:
		return float64(v.Uint16())
	case proto.TypeInt32:
		return float64(v.Int32())
	case proto.TypeUint32:
		return float64(v.Uint32())
	}
	return float64(v.Uint32())
}

func formatField(field proto.Field) string {
	if !field.Value.Valid(field.BaseType) {
		return "(invalid)"
	}

	if isTimestampField(field.Name) {
		t := datetime.ToTime(field.Value.Uint32())
		if field.Name == "local_timestamp" {
			return t.UTC().Format("2006-01-02 15:04:05")
		}
		return t.Local().Format("2006-01-02 15:04:05")
	}

	scale := field.Scale
	if scale == 0 {
		scale = 1
	}

	if field.Units == "s" {
		secs := numericValue(field.Value) / scale
		return formatDuration(time.Duration(secs * float64(time.Second)))
	}

	if scale != 1 {
		return fmt.Sprintf("%g", numericValue(field.Value)/scale)
	}

	return fmt.Sprintf("%v", field.Value)
}

func compareMesgs(orig, recomp proto.Message, labelA, labelB string) {
	recompByNum := make(map[byte]proto.Field)
	for _, f := range recomp.Fields {
		recompByNum[f.Num] = f
	}
	origByNum := make(map[byte]bool)
	for _, f := range orig.Fields {
		origByNum[f.Num] = true
	}

	colA := max(20, len(labelA))
	colB := max(20, len(labelB))
	rowFmt := fmt.Sprintf("%%-%ds  %%%ds  %%%ds %%s\n", 30, colA, colB)
	headFmt := fmt.Sprintf("%%-%ds  %%%ds  %%%ds\n", 30, colA, colB)

	fmt.Printf(headFmt, "field", labelA, labelB)
	fmt.Printf(headFmt, repeatStr("-", 30), repeatStr("-", colA), repeatStr("-", colB))

	for _, origField := range orig.Fields {
		origVal := formatField(origField)
		recompVal := "-"
		if rf, ok := recompByNum[origField.Num]; ok {
			recompVal = formatField(rf)
		}
		marker := " "
		if origVal != recompVal {
			marker = "*"
		}
		fmt.Printf(rowFmt, origField.Name, origVal, recompVal, marker)
	}

	for _, recompField := range recomp.Fields {
		if origByNum[recompField.Num] {
			continue
		}
		fmt.Printf(rowFmt, recompField.Name, "-", formatField(recompField), "*")
	}
}

func dumpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dump <input.fit>",
		Short: "Print all messages and their fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fit, err := decodeFIT(args[0])
			if err != nil {
				return err
			}

			counts := make(map[typedef.MesgNum]int)
			for _, mesg := range fit.Messages {
				i := counts[mesg.Num]
				counts[mesg.Num]++

				fmt.Printf("[%s #%d]\n", mesg.Num, i)
				for _, field := range mesg.Fields {
					value := formatField(field)
					if field.Units != "" {
						fmt.Printf("  %s: %s [%s]\n", field.Name, value, field.Units)
					} else {
						fmt.Printf("  %s: %s\n", field.Name, value)
					}
				}
				fmt.Println()
			}
			return nil
		},
	}
}
