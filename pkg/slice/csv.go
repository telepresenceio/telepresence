package slice

import (
	"bytes"
	"encoding/csv"
	"strings"
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
