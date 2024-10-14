package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
)

const logsFileName = "tests.log"

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
	ctx, cancel := context.WithCancel(context.Background())
	_, isCi := os.LookupEnv("GITHUB_SHA")
	progressBar := newProgressBar(ctx, isCi)
	logger, err := NewLogger(ctx, logsFileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create logger: %s\n", err)
		os.Exit(1)
	}
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
			if line.Test == "" {
				continue
			}
			progressBar.ReportCh <- line
			logger.Report(line)
		}
	}()
	progressBar.Wait()
	if logger.ReportFailures() {
		os.Exit(1)
	}
}
