package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/datawire/teleproxy/pkg/kubeapply"
	"github.com/datawire/teleproxy/pkg/tpu"
)

func envBool(name string) bool {
	val := os.Getenv(name)
	switch strings.TrimSpace(strings.ToLower(val)) {
	case "true":
		return true
	case "yes":
		return true
	case "1":
		return true
	case "false":
		return false
	case "no":
		return false
	case "0":
		return false
	case "":
		return false
	}

	return true
}

var Version = "(unknown version)"
var show_version = flag.Bool("version", false, "output version information and exit")
var debug = flag.Bool("debug", envBool("KUBEAPPLY_DEBUG"), "enable debug mode, expanded files will be preserved")
var timeout = flag.Int("t", 60, "timeout in seconds")
var files tpu.ArrayFlags

func _main() int {
	flag.Var(&files, "f", "path to yaml file")
	flag.Parse()

	if *show_version {
		fmt.Println("kubeapply", "version", Version)
		return 0
	}

	if len(files) == 0 {
		log.Print("ERROR: at least one file argument is required")
		return 1
	}

	err := kubeapply.Kubeapply(nil, time.Duration(*timeout)*time.Second, *debug, files...)
	if err != nil {
		log.Print(err)
		return 1
	}

	return 0
}

func main() {
	os.Exit(_main())
}
