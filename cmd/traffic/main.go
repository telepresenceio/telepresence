package main

import (
	"os"
	"path/filepath"
)

func main() {
	switch name := filepath.Base(os.Args[0]); name {
	case "traffic-manager":
		fallthrough
	default:
		manager_main()
	}
}
