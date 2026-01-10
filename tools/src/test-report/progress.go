package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

const (
	START      = "start"
	RUN        = "run"
	FAIL       = "fail"
	BUILD_FAIL = "build-fail"
	PASS       = "pass"
	SKIP       = "skip"
	OUTPUT     = "output"

	passed  = "✅"
	failed  = "❌"
	skipped = "🔶"

	ciRefreshRate = 5 * time.Second
)

type progressBar struct {
	sync.RWMutex
	*mpb.Progress
	bar      *mpb.Bar
	ReportCh chan *Line

	// These are lock-protected
	currentTest     string
	resultsCounters map[string]int
	askingForPw     bool
}

func (p *progressBar) End() {
	p.bar.SetTotal(-1, true)
}

func (p *progressBar) monitorProgress(ctx context.Context) {
	var pwTimer *time.Timer
	for {
		select {
		case line := <-p.ReportCh:
			switch line.Action {
			case RUN:
				p.Lock()
				p.currentTest = line.Test
				p.Unlock()
			case OUTPUT:
				if strings.Contains(line.Output, "Asking for admin credentials") {
					pwTimer = time.AfterFunc(500*time.Millisecond, func() {
						p.Lock()
						p.askingForPw = true
						p.Unlock()
					})
				} else if strings.Contains(line.Output, "Admin credentials acquired") {
					if pwTimer != nil {
						pwTimer.Stop()
						pwTimer = nil
					}
					p.Lock()
					p.askingForPw = false
					p.Unlock()
				}
			case BUILD_FAIL, FAIL, PASS, SKIP:
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

func (p *progressBar) renderCurrentTest(s decor.Statistics) string {
	p.RLock()
	ct := p.currentTest
	p.RUnlock()
	if ct == "" {
		return " starting… "
	}
	// Truncate to the left.
	if of := len(ct) + 1 - s.AvailableWidth; of > 0 {
		ct = "…" + ct[of+1:]
	}
	return " " + ct
}

func (p *progressBar) renderResults(_ decor.Statistics) string {
	p.RLock()
	defer p.RUnlock()
	out := bytes.Buffer{}
	out.WriteByte('(')
	out.WriteString(color.GreenString("%s %d passed", passed, p.resultsCounters[PASS]))
	out.WriteByte(' ')
	if buildFail := p.resultsCounters[BUILD_FAIL]; buildFail > 0 {
		out.WriteString(color.MagentaString("%s %d build failures", failed, buildFail))
		out.WriteByte(' ')
	}
	out.WriteString(color.RedString("%s %d failed", failed, p.resultsCounters[FAIL]))
	out.WriteByte(' ')
	out.WriteString(color.YellowString("%s %d skipped", skipped, p.resultsCounters[SKIP]))
	out.WriteByte(')')
	return out.String()
}

func (p *progressBar) renderPasswordPrompt(_ decor.Statistics) string {
	// decor.OnPredicate doesn't work for this somehow, so we do it manually
	p.RLock()
	defer p.RUnlock()
	if p.askingForPw {
		return color.HiRedString(" Please type in your password and hit enter! ")
	}
	return ""
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
	}
	p.bar = progress.AddSpinner(-1,
		mpb.AppendDecorators(
			decor.CurrentNoUnit(" %d "),
			decor.Any(p.renderResults),
			decor.Any(p.renderPasswordPrompt),
			decor.OnComplete(decor.Elapsed(decor.ET_STYLE_GO), "done!"),
			decor.Any(p.renderCurrentTest),
		),
		mpb.BarWidth(1),
	)
	go p.monitorProgress(ctx)
	return p
}
