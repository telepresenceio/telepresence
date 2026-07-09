package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// defaultLogsFileName is used when the TEST_LOG_OUTPUT environment variable is unset.
const defaultLogsFileName = "tests.log"

type TestID struct {
	Package string `json:"Package,omitempty"`
	Test    string `json:"Test,omitempty"`
}

type Line struct {
	TestID  `json:",inline"`
	Action  string  `json:"Action,omitempty"`
	Output  string  `json:"Output,omitempty"`
	Elapsed float64 `json:"Elapsed,omitempty"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-scope" {
		os.Exit(runScope(os.Args[2:]))
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, isCi := os.LookupEnv("GITHUB_SHA")
	progressBar := newProgressBar(ctx, isCi)
	logsFileName := defaultLogsFileName
	if out := os.Getenv("TEST_LOG_OUTPUT"); out != "" {
		logsFileName = out
	}
	logger, err := NewLogger(ctx, logsFileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create logger: %s\n", err)
		os.Exit(1)
	}
	collector := NewCollector()
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		defer func() {
			logger.CloseAndWait()
			// This ends the progress bar and hence the program
			progressBar.End()
			cancel()
		}()
		for scanner.Scan() {
			line := &Line{}
			err := json.Unmarshal(scanner.Bytes(), line)
			if err != nil {
				fmt.Fprintf(progressBar, "Failed to unmarshal line: %s\n", err)
				continue
			}
			progressBar.ReportCh <- line
			logger.Report(line)
			collector.Report(line)
		}
	}()
	progressBar.Wait()
	if failuresOut := os.Getenv("TEST_FAILURES_OUT"); failuresOut != "" {
		if err := WriteFailuresFile(failuresOut, collector.Document()); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write failures file: %s\n", err)
		}
	}
	if logger.ReportFailures() {
		os.Exit(1)
	}
}

// runScope implements `test-report -scope <failures.json>`: it reads a
// FailuresDocument and prints the KEY=VALUE lines a caller can eval to scope
// a re-run, or nothing when the failure set is unscopeable. It returns the
// process exit code; non-zero only for a missing or corrupt failures file.
func runScope(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: test-report -scope <failures.json>")
		return 1
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read failures file: %s\n", err)
		return 1
	}
	var doc FailuresDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse failures file: %s\n", err)
		return 1
	}
	for _, line := range BuildScopeOutput(doc) {
		fmt.Println(line)
	}
	return 0
}
