package main

import (
	"flag"
	"log"
	"os"
	"time"

	"github.com/datawire/teleproxy/internal/pkg/tpu"

	"github.com/datawire/teleproxy/pkg/k8s/waiter"
)

var timeout = flag.Int("t", 60, "timeout in seconds")
var files tpu.ArrayFlags

func main() {
	flag.Var(&files, "f", "path to yaml file")
	flag.Parse()

	w := waiter.NewWaiter(nil)

	err := w.ScanPaths(files)
	if err != nil {
		log.Fatal(err)
	}

	for _, resource := range flag.Args() {
		err := w.Add(resource)
		if err != nil {
			log.Fatal(err)
		}
	}

	if w.Wait(time.Duration(*timeout) * time.Second) {
		os.Exit(0)
	} else {
		os.Exit(1)
	}
}
