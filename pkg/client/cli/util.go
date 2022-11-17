package cli

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

func kubeFlagMap(kubeFlags *pflag.FlagSet) map[string]string {
	kubeFlagMap := make(map[string]string, kubeFlags.NFlag())
	kubeFlags.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			var v string
			if sv, ok := flag.Value.(pflag.SliceValue); ok {
				v = AsCSV(sv.GetSlice())
			} else {
				v = flag.Value.String()
			}
			kubeFlagMap[flag.Name] = v
		}
	})
	return kubeFlagMap
}
