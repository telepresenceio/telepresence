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
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/morikuni/aec"
)

// EventStatus indicates the status of an action.
type EventStatus int

const (
	EventStatusWorking EventStatus = iota
	EventStatusDone
	EventStatusInfo
	EventStatusWarning
	EventStatusError
)

func (s EventStatus) color() aec.ANSI {
	switch s {
	case EventStatusDone:
		return successColor
	case EventStatusInfo:
		return infoColor
	case EventStatusWarning:
		return warningColor
	case EventStatusError:
		return errorColor
	default:
		return noColor{}
	}
}

func (s EventStatus) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, s.String())
}

func (s EventStatus) String() string {
	switch s {
	case EventStatusDone:
		return "Done"
	case EventStatusWarning:
		return "Warning"
	case EventStatusError:
		return "Error"
	case EventStatusInfo:
		return "Info"
	default:
		return "Working"
	}
}

// Event represents a progress event.
type Event struct {
	ID          string      `json:"id,omitempty"`
	Text        string      `json:"text,omitempty"`
	Status      EventStatus `json:"status,omitempty"`
	StatusText  string      `json:"statusText,omitempty"`
	Current     int64       `json:"current,omitempty"`
	Percent     int         `json:"percent,omitempty"`
	Total       int64       `json:"total,omitempty"`
	StartTime   time.Time   `json:"startTime,omitzero"`
	EndTime     time.Time   `json:"endTime,omitzero"`
	plainAlways bool
	spinner     *spinner
	children    []*Event
}

// ErrorMessageEvent creates a new Error Event with a message.
func ErrorMessageEvent(id string, msg string) *Event {
	return NewEvent(id, EventStatusError, msg)
}

// WarningMessageEvent creates a new Error Event with a message.
func WarningMessageEvent(id string, msg string) *Event {
	return NewEvent(id, EventStatusWarning, msg)
}

// InfoMessageEvent creates a new Error Event with a message.
func InfoMessageEvent(id string, msg string) *Event {
	return NewEvent(id, EventStatusInfo, msg)
}

// StartingEvent creates a new Starting in progress Event.
func StartingEvent(id string) *Event {
	return NewEvent(id, EventStatusWorking, "Starting")
}

// StartedEvent creates a new Started in progress Event.
func StartedEvent(id string) *Event {
	return NewEvent(id, EventStatusDone, "Started")
}

// StoppedEvent creates a new Stopping in progress Event.
func StoppedEvent(id string) *Event {
	return NewEvent(id, EventStatusDone, "Stopped")
}

// BuildingEvent creates a new Building in progress Event.
func BuildingEvent(id string) *Event {
	return NewEvent(id, EventStatusWorking, "Building")
}

// BuiltEvent creates a new built (done) *Event.
func BuiltEvent(id string) *Event {
	return NewEvent(id, EventStatusDone, "Built")
}

// WorkingEvent creates a new <verb> in progress Event.
func WorkingEvent(id, verb string) *Event {
	return NewEvent(id, EventStatusWorking, verb)
}

// DoneEvent creates a new <verb> done Event.
func DoneEvent(id, verb string) *Event {
	return NewEvent(id, EventStatusDone, verb)
}

func NewEvent(id string, status EventStatus, statusText string) *Event {
	e := &Event{
		ID:         id,
		Status:     status,
		StatusText: statusText,
	}
	switch status {
	case EventStatusWorking:
		e.spinner = newSpinner()
		e.StartTime = time.Now()
	case EventStatusDone, EventStatusError:
		e.EndTime = time.Now()
	default:
	}
	return e
}

func (e *Event) WithText(msg string) *Event {
	e.Text = msg
	return e
}

func (e *Event) AddChild(status EventStatus, text, statusText string) *Event {
	child := NewEvent(fmt.Sprintf("%s-%d", e.ID, len(e.children)+1), status, statusText)
	child.Text = text
	e.children = append(e.children, child)
	return child
}

// PlainAlways configures the event to always be printed when using the plain progress writer.
func (e *Event) PlainAlways() *Event {
	e.plainAlways = true
	return e
}

func (e *Event) Pump(ctx context.Context, status EventStatus) io.Writer {
	in, out := io.Pipe()
	wr := ContextWriter(ctx)
	id := e.ID
	if id == "" {
		id = EventId(ctx)
	}
	go func() {
		for {
			var buf [1024]byte
			n, err := in.Read(buf[:])
			if n > 0 {
				wr.Write(NewEvent(id, status, strings.TrimSpace(string(buf[:n]))))
			}
			if err != nil {
				return
			}
		}
	}()
	return out
}

func (e *Event) stop() {
	e.EndTime = time.Now()
	if e.spinner != nil {
		e.spinner.Stop()
	}
}

func (e *Event) child(id string) *Event {
	for _, child := range e.children {
		if child.ID == id {
			return child
		}
	}
	return nil
}

func (e *Event) merge(o *Event) {
	if e == o {
		return
	}
	e.Text = o.Text
	e.EndTime = o.EndTime

	switch e.Status {
	case EventStatusInfo, EventStatusWarning, EventStatusError:
		if o.Status == EventStatusWorking || o.Status == EventStatusDone {
			ch := e.AddChild(e.Status, e.Text, e.StatusText)
			ch.plainAlways = e.plainAlways
		}
	default:
	}

	switch o.Status {
	case EventStatusError:
		e.Status = EventStatusError
		e.stop()
		fallthrough
	case EventStatusInfo, EventStatusWarning:
		e.AddChild(o.Status, o.Text, o.StatusText)
	case EventStatusDone:
		e.stop()
		fallthrough
	case EventStatusWorking:
		e.spinner = o.spinner
		e.Text = o.Text
		e.Status = o.Status
		e.StatusText = o.StatusText
		// progress can only go up
		if o.Total > e.Total {
			e.Total = o.Total
		}
		if o.Current > e.Current {
			e.Current = o.Current
		}
		if o.Percent > e.Percent {
			e.Percent = o.Percent
		}
	}

	// Drop Working and Done events from the current event. Merge other events
	// with the same ID.
	var children []*Event
	for _, child := range e.children {
		if oc := o.child(child.ID); oc != nil {
			child.merge(oc)
			children = append(children, child)
		} else {
			switch child.Status {
			case EventStatusWorking, EventStatusDone:
			default:
				children = append(children, child)
			}
		}
	}
	// Add new events.
	for _, child := range o.children {
		if ec := e.child(child.ID); ec == nil {
			children = append(children, child)
		}
	}

	if e.Status == EventStatusDone {
		for _, child := range children {
			if child.Status == EventStatusWorking {
				child.Status = EventStatusDone
			}
		}
	}
	e.children = children
}

const (
	spinnerDone    = "✔"
	spinnerWarning = "!"
	spinnerError   = "✘"
)

func (e *Event) Spinner() string {
	switch e.Status {
	case EventStatusDone:
		return successColor.Apply(spinnerDone)
	case EventStatusWarning:
		return warningColor.Apply(spinnerWarning)
	case EventStatusError:
		return errorColor.Apply(spinnerError)
	default:
		if e.spinner == nil {
			return " "
		}
		return countColor.Apply(e.spinner.String())
	}
}
