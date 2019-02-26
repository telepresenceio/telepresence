// Copyright 2018 Datawire. All rights reserved.

// +build ignore

// gotest2tap.go translates `go test -json` on stdin to TAP v13 on
// stdout.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// TestEvent is taken verbatim from `go doc test2json`
type TestEvent struct {
	Time    time.Time // encodes as an RFC3339-format string
	Action  string
	Package string
	Test    string
	Elapsed float64 // seconds
	Output  string
}

func main() {
	fmt.Println("TAP version 13")

	testCnt := 0
	bailed := false

	stdin := bufio.NewScanner(os.Stdin)
	for stdin.Scan() {
		var event TestEvent
		if err := json.Unmarshal(stdin.Bytes(), &event); err != nil {
			fmt.Printf("Bail out! Invalid JSON: %v: %q\n", err, stdin.Text())
			bailed = true
		}

		Time := event.Time.Format("2006-01-02T15:04:05.000000000")
		Elapsed := time.Duration(float64(time.Second) * event.Elapsed)
		Output := strings.TrimSuffix(event.Output, "\n")
		if event.Test == "" {
			fmt.Println("#",
				Time,
				"(took "+Elapsed.String()+")",
				fmt.Sprintf("%-6s", event.Action),
				event.Package,
				Output)
		} else {
			Name := event.Package + "." + event.Test
			// TODO(lukeshu): I think maybe this should also handel "bench"?
			switch event.Action {
			case "pass":
				testCnt++
				fmt.Printf("ok %d %v # %v (%v) %v\n", testCnt, Name, Time, Elapsed, Output)
			case "fail":
				testCnt++
				fmt.Printf("not ok %d %v.%v # %v (%v) %v\n", testCnt, Name, Time, Elapsed, Output)
			case "skip":
				testCnt++
				fmt.Printf("ok %d %v.%v # SKIP %v (%v) %v\n", testCnt, Name, Time, Elapsed, Output)
			default:
				fmt.Sprintln("#",
					Time,
					"(took "+Elapsed.String()+")",
					fmt.Sprintf("%-6s", event.Action),
					Name,
					Output)
			}
		}
	}
	if err := stdin.Err(); err != nil {
		fmt.Println("Bail out!", err)
		bailed = true
	}
	if !bailed {
		fmt.Printf("1..%d\n", testCnt)
	}
}
