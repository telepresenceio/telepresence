package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var dest string
	flag.StringVar(&dest, "d", "1", "Destination of output. Legal values are 1 (stdout), 2 (stderr) or a file name")
	flag.Parse()

	var out *os.File
	switch dest {
	case "1":
		out = os.Stdout
	case "2":
		out = os.Stderr
	default:
		var err error
		if out, err = os.Create(dest); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		defer out.Close()
	}
	for _, s := range flag.Args() {
		fmt.Fprintln(out, s)
	}
}
