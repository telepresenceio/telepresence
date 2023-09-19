package ioutil

import (
	"fmt"
	"io"
	"strings"
)

// Print is like Fprint but panics on error.
func Print(out io.Writer, txt string) int {
	n, err := fmt.Fprint(out, txt)
	if err != nil {
		panic(err)
	}
	return n
}

// Println is like Fprintln but panics on error.
func Println(out io.Writer, txt string) int {
	n, err := fmt.Fprintln(out, txt)
	if err != nil {
		panic(err)
	}
	return n
}

// Printf is like Fprintf but panics on error.
func Printf(out io.Writer, format string, args ...any) int {
	n, err := fmt.Fprintf(out, format, args...)
	if err != nil {
		panic(err)
	}
	return n
}

// WriteString is like io.WriteString but panics on error.
func WriteString(out io.Writer, s string) int {
	n, err := io.WriteString(out, s)
	if err != nil {
		panic(err)
	}
	return n
}

func WriterToString(wt func(w io.Writer) (int64, error)) string {
	var sb strings.Builder
	_, err := wt(&sb)
	if err != nil {
		panic(err)
	}
	return sb.String()
}
