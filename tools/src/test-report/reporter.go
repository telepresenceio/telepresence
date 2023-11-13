package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/datawire/metriton-go-client/metriton"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

const (
	reportChanSz   = 1024 // Big buffer to avoid blocking the test process
	requestQueueSz = 10
)

type Reporter struct {
	metriton.Reporter

	// Github metadata
	arch       string
	os         string
	sha        string
	actor      string
	repository string
	actionName string
	attempt    int

	total        int64
	reportCh     chan *Line
	bar          *mpb.Bar
	doneCh       chan struct{}
	onError      func(error)
	requestQueue chan *Line
}

func NewReporter(ctx context.Context, progress *progressBar, onError func(error)) (*Reporter, error) {
	r := &Reporter{
		reportCh:     make(chan *Line, reportChanSz),
		doneCh:       make(chan struct{}),
		onError:      onError,
		requestQueue: make(chan *Line, requestQueueSz),
	}
	// Populate the reporter from the github actions environment
	var ok bool
	if r.sha, ok = os.LookupEnv("GITHUB_SHA"); !ok {
		return nil, nil
	}
	r.arch = os.Getenv("RUNNER_OS")
	r.os = os.Getenv("RUNNER_OS")
	var err error
	r.attempt, err = strconv.Atoi(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse GITHUB_RUN_ATTEMPT: %w", err)
	}
	r.actor = os.Getenv("GITHUB_ACTOR")
	r.repository = os.Getenv("GITHUB_REPOSITORY")
	r.actionName = os.Getenv("GITHUB_ACTION")

	r.bar = progress.AddBar(0, mpb.PrependDecorators(
		decor.Name("Metriton reporting: "),
		decor.CountersNoUnit(" %d / %d "),
		decor.AverageETA(decor.ET_STYLE_GO),
	))

	r.Reporter = metriton.Reporter{
		Application: "telepresence-tests",
		Version:     "v0.0.0",
		GetInstallID: func(*metriton.Reporter) (string, error) {
			return "00000000-0000-0000-0000-000000000000", nil
		},
		Endpoint: metriton.DefaultEndpoint,
		BaseMetadata: map[string]interface{}{
			"sha":        r.sha,
			"arch":       r.arch,
			"os":         r.os,
			"attempt":    r.attempt,
			"actor":      r.actor,
			"repository": r.repository,
			"action":     r.actionName,
		},
	}
	go r.run(ctx)
	return r, nil
}

func (r *Reporter) Report(line *Line) {
	if r == nil || line.Action == OUTPUT {
		return
	}
	r.reportCh <- line
	r.total++
	r.bar.SetTotal(r.total, false)
}

func (r *Reporter) sendRequests(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case line, ok := <-r.requestQueue:
			if !ok {
				return
			}
			_, err := r.Reporter.Report(ctx, map[string]interface{}{
				"test":    line.Test,
				"package": line.Package,
				"action":  line.Action,
				"elapsed": line.Elapsed,
			})
			if err != nil {
				r.onError(err)
			}
			r.bar.Increment()
		case <-ctx.Done():
			return
		}
	}
}

func (r *Reporter) run(ctx context.Context) {
	var wg sync.WaitGroup
	defer func() {
		close(r.requestQueue)
		wg.Wait()
		close(r.doneCh)
	}()
	wg.Add(requestQueueSz)
	for i := 0; i < requestQueueSz; i++ {
		go r.sendRequests(ctx, &wg)
	}
	for {
		select {
		case line, ok := <-r.reportCh:
			if !ok {
				return
			}
			r.requestQueue <- line
		case <-ctx.Done():
			return
		}
	}
}

func (r *Reporter) CloseAndWait() {
	if r == nil {
		return
	}
	close(r.reportCh)
	<-r.doneCh
}
