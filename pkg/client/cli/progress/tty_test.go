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
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLineText(t *testing.T) {
	now := time.Now()
	ev := &Event{
		ID:         "id",
		Text:       "Text",
		Status:     EventStatusWorking,
		StatusText: "Status",
		EndTime:    now,
		StartTime:  now,
		spinner: &spinner{
			chars: []string{"."},
		},
	}

	lineWidth := len(ev.Text)

	out, n := tty().lineText(ev, false, 50, lineWidth)
	assert.Equal(t, " \x1b[33m.\x1b[0m Text Status                              \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)

	ev.Status = EventStatusDone
	out, n = tty().lineText(ev, false, 50, lineWidth)
	assert.Equal(t, " \x1b[32m✔\x1b[0m Text \x1b[32mStatus\x1b[0m                              \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)

	ev.Status = EventStatusError
	out, n = tty().lineText(ev, false, 50, lineWidth)
	assert.Equal(t, " \x1b[31m\x1b[1m✘\x1b[0m Text \x1b[31m\x1b[1mStatus\x1b[0m                              \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)

	ev.Status = EventStatusWarning
	out, n = tty().lineText(ev, false, 50, lineWidth)
	assert.Equal(t, " \x1b[33m\x1b[1m!\x1b[0m Text \x1b[33m\x1b[1mStatus\x1b[0m                              \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)

	ev.Status = EventStatusWorking
	ev.Text = ""
	out, n = tty().lineText(ev, false, 50, 0)
	assert.Equal(t, " \x1b[33m.\x1b[0m Status                                   \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)

	ev.Text = "Text"
	lineWidth = len(fmt.Sprintf("%s %s ", ev.ID, ev.Text))

	out, n = tty().lineText(ev, true, 50, lineWidth)
	assert.Equal(t, " \x1b[33m.\x1b[0m id Text Status                           \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)

	ev.Status = EventStatusDone
	out, n = tty().lineText(ev, true, 50, lineWidth)
	assert.Equal(t, " \x1b[32m✔\x1b[0m id Text \x1b[32mStatus\x1b[0m                           \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)

	ev.Status = EventStatusError
	out, n = tty().lineText(ev, true, 50, lineWidth)
	assert.Equal(t, " \x1b[31m\x1b[1m✘\x1b[0m id Text \x1b[31m\x1b[1mStatus\x1b[0m                           \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)

	ev.Status = EventStatusWarning
	out, n = tty().lineText(ev, true, 50, lineWidth)
	assert.Equal(t, " \x1b[33m\x1b[1m!\x1b[0m id Text \x1b[33m\x1b[1mStatus\x1b[0m                           \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)
}

func TestEventTruncate(t *testing.T) {
	now := time.Now()
	ev := &Event{
		ID:         "id",
		Text:       "Text",
		Status:     EventStatusWorking,
		StatusText: "Long status text that should be truncated",
		EndTime:    now,
		StartTime:  now,
		spinner: &spinner{
			chars: []string{"."},
		},
	}

	lineWidth := len(fmt.Sprintf("%s %s ", ev.ID, ev.Text))
	out, n := tty().lineText(ev, true, 40, lineWidth)
	assert.Equal(t, " \x1b[33m.\x1b[0m id Text Long status text that  \x1b[34m0.0s \x1b[0m\x1b[0K\n           should be truncated\x1b[0K\n", out)
	assert.Equal(t, 2, n)

	ev.Status = EventStatusDone
	out, n = tty().lineText(ev, true, 40, lineWidth)
	assert.Equal(t, " \x1b[32m✔\x1b[0m id Text \x1b[32mLong status text that\x1b[0m  \x1b[34m0.0s \x1b[0m\x1b[0K\n           \x1b[32mshould be truncated\x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 2, n)
}

func TestErrorEventWrap(t *testing.T) {
	now := time.Now()
	ev := &Event{
		ID:         "id",
		Text:       "Text",
		Status:     EventStatusError,
		StatusText: "Long status text that should be wrapped",
		EndTime:    now,
		StartTime:  now,
		spinner: &spinner{
			chars: []string{"."},
		},
	}

	lineWidth := len(fmt.Sprintf("%s %s ", ev.ID, ev.Text))
	out, n := tty().lineText(ev, true, 40, lineWidth)
	assert.Equal(t, " \x1b[31m\x1b[1m✘\x1b[0m id Text \x1b[31m\x1b[1mLong status text that\x1b[0m  \x1b[34m0.0s \x1b[0m\x1b[0K\n           \x1b[31m\x1b[1mshould be wrapped\x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 2, n)
}

func TestLineTextSingleEvent(t *testing.T) {
	now := time.Now()
	ev := &Event{
		ID:         "id",
		Text:       "Text",
		Status:     EventStatusDone,
		StatusText: "Status",
		StartTime:  now,
		EndTime:    now,
		spinner: &spinner{
			chars: []string{"."},
		},
	}

	lineWidth := len(fmt.Sprintf("%s %s", ev.ID, ev.Text))

	out, n := tty().lineText(ev, true, 50, lineWidth)
	assert.Equal(t, " \x1b[32m✔\x1b[0m id Text \x1b[32mStatus\x1b[0m                           \x1b[34m0.0s \x1b[0m\x1b[0K\n", out)
	assert.Equal(t, 1, n)
}

func TestErrorEvent(t *testing.T) {
	w := tty()
	e := &Event{
		ID:         "id",
		Text:       "Text",
		Status:     EventStatusWorking,
		StatusText: "Working",
		StartTime:  time.Now().Add(-1 * time.Second),
		spinner: &spinner{
			chars: []string{"."},
		},
	}
	// Fire "Working" event and check end time isn't touched
	w.Write(e)
	event, ok := w.events[e.ID]
	assert.True(t, ok)
	assert.True(t, event.EndTime.Equal(time.Time{}))

	// Fire "Error" event and check end time is set
	e = &Event{
		ID:         "id",
		Text:       "Text",
		Status:     EventStatusError,
		StatusText: "Working",
		StartTime:  time.Now(),
		spinner: &spinner{
			chars: []string{"."},
		},
	}
	w.Write(e)
	event, ok = w.events[e.ID]
	assert.True(t, ok)
	assert.True(t, event.EndTime.After(event.StartTime))
}

func TestWarningEvent(t *testing.T) {
	w := tty()
	e := &Event{
		ID:         "id",
		Text:       "Text",
		Status:     EventStatusWorking,
		StatusText: "Working",
		StartTime:  time.Now(),
		spinner: &spinner{
			chars: []string{"."},
		},
	}
	// Fire "Working" event and check end time isn't touched
	w.Write(e)
	event, ok := w.events[e.ID]
	assert.True(t, ok)
	assert.True(t, event.EndTime.Equal(time.Time{}))

	// Fire "Warning" event and check end time isn't touched
	e = &Event{
		ID:         "id",
		Text:       "Text",
		Status:     EventStatusWarning,
		StatusText: "Working",
		StartTime:  time.Now(),
		spinner: &spinner{
			chars: []string{"."},
		},
	}
	w.Write(e)
	event, ok = w.events[e.ID]
	assert.True(t, ok)
	assert.True(t, event.EndTime.Equal(time.Time{}))
}

func tty() *ttyWriter {
	return newTTYWriter(os.Stderr).(*ttyWriter)
}

// syncBuffer wraps a bytes.Buffer with a mutex so it can be written from the
// ticker goroutine while the test reads its length from the main goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// TestStopStopsTicker verifies that Stop() terminates the goroutine started
// by Start(), so a TriggerRefresh() after Stop() does not keep re-rendering,
// and that a subsequent Start()/Stop() cycle still works.
func TestStopStopsTicker(t *testing.T) {
	buf := &syncBuffer{}
	w := newTTYWriter(buf).(*ttyWriter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ev := func(id string) *Event {
		return &Event{
			ID:        id,
			Text:      "Text",
			Status:    EventStatusWorking,
			StartTime: time.Now(),
			spinner:   &spinner{chars: []string{"."}},
		}
	}

	w.Start(ctx, "Test")
	w.Write(ev("id1"))
	time.Sleep(500 * time.Millisecond)
	w.Stop()

	n := buf.Len()
	assert.Positive(t, n)

	w.TriggerRefresh()
	time.Sleep(800 * time.Millisecond)
	assert.Equal(t, n, buf.Len(), "goroutine kept rendering after Stop")

	// A second Start/Stop cycle should still render and stop cleanly.
	w.Start(ctx, "Test")
	w.Write(ev("id2"))
	time.Sleep(500 * time.Millisecond)
	assert.Greater(t, buf.Len(), n, "second Start did not render")

	w.Stop()
	n = buf.Len()

	w.TriggerRefresh()
	time.Sleep(800 * time.Millisecond)
	assert.Equal(t, n, buf.Len(), "goroutine kept rendering after second Stop")
}
