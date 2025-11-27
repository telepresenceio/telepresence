//go:build go1.24

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/cmd/cobraparser/v2/types"
)

func main() {
	fs := pflag.NewFlagSet("cobraparse", pflag.ExitOnError)
	var helpArg string
	fs.BoolP("help", "h", false, "Help for this command")
	fs.StringVar(&helpArg, "help-arg", "--help", "Help for this argument")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage:\n  cobraparse <command to parse help output from> [...args]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])
	args := fs.Args()
	if len(args) == 0 {
		fs.Usage()
		os.Exit(2)
	}
	f := types.HelpFetcher{
		Executable: args[0],
		HelpArg:    helpArg,
	}
	root, err := f.BuildCommandTree(args[1:], 8)
	if err != nil {
		log.Fatalf("error: %v\n", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		log.Fatalf("error writing JSON: %v\n", err)
	}
}
