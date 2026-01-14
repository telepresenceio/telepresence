/*
   Copyright 2020 Docker Compose CLI authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package progress

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/docker/go-units"
	"github.com/mitchellh/go-wordwrap"
	"github.com/moby/term"
	"github.com/morikuni/aec"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type ttyWriter struct {
	out             io.Writer
	ticker          *time.Ticker
	events          map[string]*Event
	eventIDs        []string
	repeated        bool
	numLines        int
	doneOnce        sync.Once
	done            chan struct{}
	mtx             sync.Mutex
	skipChildEvents bool
	progressTitle   string
}

func newTTYWriter(out io.Writer) Writer {
	w := &ttyWriter{
		out:    out,
		events: make(map[string]*Event),
		done:   make(chan struct{}),
	}
	w.ticker = time.NewTicker(math.MaxInt64)
	return w
}

func (w *ttyWriter) Start(ctx context.Context, progressTitle string) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	w.events = make(map[string]*Event)
	w.eventIDs = nil
	w.repeated = false
	w.numLines = 0
	w.done = make(chan struct{})
	w.skipChildEvents = false
	w.progressTitle = progressTitle
	go func() {
		defer w.ticker.Stop()
		for {
			select {
			case <-w.ticker.C:
				w.print()
			case <-ctx.Done():
				w.print()
				return
			case <-w.done:
				return
			}
		}
	}()
}

func (w *ttyWriter) IsNoOp() bool {
	return false
}

func (w *ttyWriter) Stop() {
	w.doneOnce.Do(func() {
		close(w.done)
	})
	w.print()
}

func (w *ttyWriter) event(e *Event) {
	last, ok := w.events[e.ID]
	if ok {
		last.merge(e)
	} else {
		w.eventIDs = append(w.eventIDs, e.ID)
		w.events[e.ID] = e
	}
}

func (w *ttyWriter) Write(events ...*Event) {
	w.mtx.Lock()
	for _, e := range events {
		w.event(e)
	}
	w.mtx.Unlock()
	w.TriggerRefresh()
}

func (w *ttyWriter) TriggerRefresh() {
	w.ticker.Reset(333 * time.Millisecond)
}

func (w *ttyWriter) print() {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if len(w.eventIDs) == 0 {
		return
	}
	ws, err := term.GetWinsize(1)
	if err != nil {
		ws = &term.Winsize{
			Height: 25,
			Width:  80,
		}
	}
	b := aec.EmptyBuilder
	if w.repeated {
		b = b.Up(uint(w.numLines))
		ioutil.Print(w.out, b.Column(0).ANSI)
	} else {
		w.repeated = true
	}

	// Hide the cursor while we are printing
	ioutil.Print(w.out, aec.Hide)
	defer func() {
		ioutil.Print(w.out, aec.Show)
	}()

	numLines := 0
	withID := len(w.eventIDs) > 1
	if withID {
		firstLine := fmt.Sprintf("[+] %s %d/%d", w.progressTitle, numDone(w.events), len(w.events))
		if numDone(w.events) == len(w.events) {
			firstLine = doneColor.Apply(firstLine)
		}
		firstLine += aec.EraseLine(aec.EraseModes.Tail).String()
		ioutil.Println(w.out, firstLine)
		numLines++
	}

	var statusPadding int
	for _, v := range w.eventIDs {
		event := w.events[v]
		l := len(event.Text)
		if withID {
			if l > 0 {
				l++ // one space between text and id
			}
			l += len(event.ID)
		}
		if l > 0 {
			l++ // one space after text
			if statusPadding < l {
				statusPadding = l
			}
		}
	}

	if len(w.eventIDs) > int(ws.Height)-2 {
		w.skipChildEvents = true
	}
	for _, v := range w.eventIDs {
		event := w.events[v]
		line, lines := w.lineText(event, withID, int(ws.Width), statusPadding)
		ioutil.Print(w.out, line)
		numLines += lines
		for _, child := range event.children {
			if w.skipChildEvents {
				continue
			}
			line, lines = w.lineText(child, false, int(ws.Width), statusPadding)
			ioutil.Print(w.out, line)
			numLines += lines
		}
	}

	for i := numLines; i < w.numLines; i++ {
		if numLines < int(ws.Height)-2 {
			ioutil.Println(w.out, aec.EraseLine(aec.EraseModes.All).String())
			numLines++
		}
	}
	w.numLines = numLines
}

var percentChars = strings.Split("⠀⡀⣀⣄⣤⣦⣶⣷⣿", "") //nolint:gochecknoglobals // constant names

func (w *ttyWriter) lineText(event *Event, withID bool, terminalWidth, statusPadding int) (string, int) {
	var (
		hideDetails bool
		total       int64
		current     int64
		completion  []string
	)

	// only show the aggregated progress while the root operation is in progress
	if event.Status == EventStatusWorking {
		for _, child := range event.children {
			if child.Status == EventStatusWorking && child.Total == 0 {
				// we don't have totals available for all the child events
				// so don't show the total progress yet
				hideDetails = true
			}
			total += child.Total
			current += child.Current
			if child.Percent > 0 {
				completion = append(completion, percentChars[(len(percentChars)-1)*child.Percent/100])
			}
		}
	}

	// don't try to show detailed progress if we don't have any idea
	if total == 0 {
		hideDetails = true
	}

	var txt string
	if len(completion) > 0 {
		var details string
		if !hideDetails {
			details = fmt.Sprintf(" %7s / %-7s ", units.HumanSize(float64(current)), units.HumanSize(float64(total)))
		}
		txt = fmt.Sprintf("[%s]%s%s",
			successColor.Apply(strings.Join(completion, "")),
			details,
			event.Text,
		)
	} else {
		txt = event.Text
	}
	if withID {
		if txt == "" {
			txt = event.ID
		} else {
			txt = fmt.Sprintf("%s %s", event.ID, txt)
		}
	}
	textLen := len(txt)
	padding := statusPadding - textLen
	if padding < 0 {
		padding = 0
	}
	if txt != "" && padding == 0 {
		padding++
	}

	// calculate the max length for the status text
	const spinnerWidth = 3 // spinner surrounded by space
	allExceptStatusLen := spinnerWidth + textLen + padding
	var timerLen int
	var timer, coloredTimer string
	switch {
	case event.Status == EventStatusWorking:
		timer = fmt.Sprintf("%.1fs ", time.Since(event.StartTime).Seconds())
	case !event.EndTime.IsZero():
		timer = fmt.Sprintf("%.1fs ", event.EndTime.Sub(event.StartTime).Seconds())
	default:
		timer = ""
	}

	if timer != "" {
		timerLen = len(timer)
		coloredTimer = timerColor.Apply(timer)
		allExceptStatusLen += timerLen
	}

	maxStatusLen := terminalWidth - allExceptStatusLen - 1 //
	if maxStatusLen < 5 {
		// This will look weird, and that's intentional when terminalWidth < 5 + allExceptStatusLen
		maxStatusLen = math.MaxInt
	}
	lines := strings.Split(wordwrap.WrapString(event.StatusText, uint(maxStatusLen)), "\n")

	bld := &strings.Builder{}
	for li, status := range lines {
		if li > 0 {
			writePad(bld, spinnerWidth+textLen)
			timerLen = 0
		} else {
			bld.WriteByte(' ')
			bld.WriteString(event.Spinner())
			bld.WriteByte(' ')
			bld.WriteString(txt)
		}
		if len(status) > 0 || timerLen > 0 {
			writePad(bld, padding)
			bld.WriteString(event.Status.color().Apply(status))
			if timerLen > 0 {
				writePad(bld, terminalWidth-allExceptStatusLen-len(status))
				bld.WriteString(coloredTimer)
			}
		}
		bld.WriteString(aec.EraseLine(aec.EraseModes.Tail).String())
		bld.WriteByte('\n')
	}
	return bld.String(), len(lines)
}

func writePad(bld *strings.Builder, padLen int) {
	for ; padLen > 0; padLen-- {
		bld.WriteByte(' ')
	}
}

func numDone(events map[string]*Event) int {
	i := 0
	for _, e := range events {
		if e.Status != EventStatusWorking {
			i++
		}
	}
	return i
}
