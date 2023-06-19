package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

const (
	START  = "start"
	RUN    = "run"
	FAIL   = "fail"
	PASS   = "pass"
	SKIP   = "skip"
	OUTPUT = "output"

	passed  = "✅"
	failed  = "❌"
	skipped = "🔶"

	ciRefreshRate = 5 * time.Second
)

type progressBar struct {
	sync.RWMutex
	*mpb.Progress
	bar            *mpb.Bar
	ReportCh       chan *Line
	failureOutputs map[TestID]string

	// These are lock-protected
	currentTest     string
	resultsCounters map[string]int
}

func (p *progressBar) PrintFailures() bool {
	if len(p.failureOutputs) == 0 {
		return false
	}
	separator := strings.Repeat("=", 200)
	fmt.Fprintf(os.Stderr, "\n\nFailed tests:\n")
	for testID, output := range p.failureOutputs {
		fmt.Fprintf(os.Stderr, "\n%s.%s\n%s\n%s\n", testID.Package, testID.Test, output, separator)
	}
	return true
}

func (p *progressBar) End() {
	p.bar.SetTotal(-1, true)
}

func (p *progressBar) monitorProgress(ctx context.Context) {
	for {
		select {
		case line := <-p.ReportCh:
			switch line.Action {
			case RUN:
				p.Lock()
				p.currentTest = line.Test
				p.Unlock()
			case OUTPUT:
				if _, ok := p.failureOutputs[line.TestID]; !ok {
					p.failureOutputs[line.TestID] = ""
				}
				p.failureOutputs[line.TestID] += line.Output
			case PASS, SKIP:
				delete(p.failureOutputs, line.TestID)
				fallthrough
			case FAIL:
				p.Lock()
				if _, ok := p.resultsCounters[line.Action]; !ok {
					p.resultsCounters[line.Action] = 0
				}
				p.resultsCounters[line.Action]++
				p.Unlock()
				p.bar.Increment()
			}
		case <-ctx.Done():
			return
		}
	}
}

func (p *progressBar) renderCurrentTest(_ decor.Statistics) string {
	p.RLock()
	defer p.RUnlock()
	if p.currentTest == "" {
		return " starting... "
	}
	return " " + p.currentTest
}

func (p *progressBar) renderResults(_ decor.Statistics) string {
	p.RLock()
	defer p.RUnlock()
	return fmt.Sprintf("(%s %s %s) ",
		color.GreenString("%s %d passed", passed, p.resultsCounters[PASS]),
		color.RedString("%s %d failed", failed, p.resultsCounters[FAIL]),
		color.YellowString("%s %d skipped", skipped, p.resultsCounters[SKIP]),
	)
}

func newProgressBar(ctx context.Context, isCi bool) *progressBar {
	opts := []mpb.ContainerOption{}
	if isCi {
		// AutoRefresh ensures CI logs are updated; refresh rate governs how often.
		opts = append(opts, mpb.WithRefreshRate(ciRefreshRate), mpb.WithAutoRefresh())
	}
	progress := mpb.NewWithContext(ctx, opts...)
	p := &progressBar{
		Progress:        progress,
		ReportCh:        make(chan *Line),
		resultsCounters: make(map[string]int),
		failureOutputs:  make(map[TestID]string),
	}
	p.bar = progress.AddSpinner(-1,
		mpb.AppendDecorators(
			decor.CurrentNoUnit(" %d "),
			decor.Any(p.renderResults),
			decor.OnComplete(decor.Elapsed(decor.ET_STYLE_GO), "done!"),
			decor.Any(p.renderCurrentTest),
		),
		mpb.BarWidth(1),
	)
	go p.monitorProgress(ctx)
	return p
}
