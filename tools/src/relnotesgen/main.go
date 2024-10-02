package main

import (
	_ "embed"
	"flag"
	"log"

	"github.com/telepresenceio/telepresence/tools/src/relnotesgen/relnotes"
)

func main() {
	var input string
	var mdx bool
	flag.StringVar(&input, "input", "", "input file")
	flag.BoolVar(&mdx, "mdx", false, "generate mdx")
	flag.Parse()
	err := relnotes.MakeReleaseNotes(input, mdx)
	if err != nil {
		log.Fatal(err)
	}
}
