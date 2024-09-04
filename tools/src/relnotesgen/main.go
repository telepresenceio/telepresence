package main

import (
	_ "embed"
	"flag"
	"log"

	"github.com/telepresenceio/telepresence/tools/src/relnotesgen/relnotes"
)

func main() {
	var input string
	flag.StringVar(&input, "input", "", "input file")
	flag.Parse()
	err := relnotes.MakeReleaseNotes(input)
	if err != nil {
		log.Fatal(err)
	}
}
