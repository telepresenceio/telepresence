package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/datawire/metriton-go-client/metriton"
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
	reporter, err := NewReporter(ctx, progressBar, func(err error) {
		fmt.Fprintf(progressBar, "Failed to report: %s\n", err)
	})
	if err != nil {
		// An error will not be reported if the reporter is disabled because not in CI.
		// We can't print to the progressBar if we're leaving, apparently.
		fmt.Fprintf(os.Stderr, "Failed to create reporter: %s\n", err)
		os.Exit(1)
	}
	logger, err := NewLogger(ctx, logsFileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create logger: %s\n", err)
		os.Exit(1)
	}
	if metriton.IsDisabledByUser() {
		fmt.Fprint(progressBar, "Reporting is disabled by user\n")
	}
	if isCi {
		fmt.Fprintf(progressBar, "Reporting to %s\n", reporter.Endpoint)
	}
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		defer func() {
			reporter.CloseAndWait()
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
			reporter.Report(line)
			progressBar.ReportCh <- line
			logger.Report(line)
		}
	}()
	progressBar.Wait()
	if logger.ReportFailures() {
		os.Exit(1)
	}
}
