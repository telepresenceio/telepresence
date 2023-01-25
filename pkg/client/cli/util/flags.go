package util

import (
	"bytes"
	"encoding/csv"
	"strings"

	"github.com/spf13/pflag"
)

// AsCSV returns the string slice encoded by a csv.NewWriter.
func AsCSV(vs []string) string {
	b := &bytes.Buffer{}
	w := csv.NewWriter(b)
	if err := w.Write(vs); err != nil {
		// The underlying bytes.Buffer should never error.
		panic(err)
	}
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

// FlagMap returns a map of the flags that has been modified in the given FlagSet.
func FlagMap(flags *pflag.FlagSet) map[string]string {
	flagMap := make(map[string]string, flags.NFlag())
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			var v string
			if sv, ok := flag.Value.(pflag.SliceValue); ok {
				v = AsCSV(sv.GetSlice())
			} else {
				v = flag.Value.String()
			}
			flagMap[flag.Name] = v
		}
	})
	return flagMap
}
