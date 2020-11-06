package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) > 1 {
		switch name := os.Args[1]; name {
		case "agent":
			agent_main()
		case "manager":
			manager_main()
		case "mech-tcp":
			mech_tcp_main()
		default:
			fmt.Println("traffic: unknown command:", name)
			os.Exit(127)
		}
		return
	}

	switch name := filepath.Base(os.Args[0]); name {
	case "traffic-agent":
		agent_main()
	case "mechanism-tcp":
		mech_tcp_main()
	case "traffic-manager":
		fallthrough
	default:
		manager_main()
	}
}
