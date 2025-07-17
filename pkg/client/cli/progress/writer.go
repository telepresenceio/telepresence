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

	"github.com/moby/term"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// Writer can write multiple progress events.
type Writer interface {
	// Start will start the writer in a new goroutine
	Start(ctx context.Context, progressTitle string)

	// Stop stops a started spinner
	Stop()

	// IsNoOp returns true for the noop spinner, false for every other spinner.
	IsNoOp() bool

	// Write writes progress events. If multiple events are given and the writer permits it, they will
	// end up in different spinners and be reported individually based on their ID.
	Write(...*Event)

	// TriggerRefresh triggers a refresh of event output on the tty writer
	TriggerRefresh()
}

type writerKey struct{}

// WithContextWriter adds the writer to the context.
func WithContextWriter(ctx context.Context, writer Writer) context.Context {
	return context.WithValue(ctx, writerKey{}, writer)
}

// ContextWriter returns the writer from the context.
func ContextWriter(ctx context.Context) Writer {
	s, ok := ctx.Value(writerKey{}).(Writer)
	if !ok {
		return noopWriter{}
	}
	return s
}

type eventIdKey struct{}

func WithEventId(ctx context.Context, id string) context.Context {
	currentID := EventId(ctx)
	if currentID == id {
		return ctx
	}
	return context.WithValue(ctx, eventIdKey{}, id)
}

func EventId(ctx context.Context) string {
	id, ok := ctx.Value(eventIdKey{}).(string)
	if !ok {
		return ""
	}
	return id
}

func IsNoOp(ctx context.Context) bool {
	return ContextWriter(ctx).IsNoOp()
}

func Start(ctx context.Context, title string) {
	w := ContextWriter(ctx)
	w.Stop()
	w.Start(ctx, title)
}

func Stop(ctx context.Context) {
	ContextWriter(ctx).Stop()
}

func Working(ctx context.Context, args ...any) *Event {
	return write(ctx, EventStatusWorking, false, args)
}

func Workingf(ctx context.Context, format string, args ...any) *Event {
	return writef(ctx, EventStatusWorking, false, format, args)
}

func Done(ctx context.Context, args ...any) *Event {
	return write(ctx, EventStatusDone, false, args)
}

func Donef(ctx context.Context, format string, args ...any) *Event {
	return writef(ctx, EventStatusDone, false, format, args)
}

// PrintDone is like Done but also enforces that the plain writer will print the output. The
// plain writer normally skips Working and Done events.
func PrintDone(ctx context.Context, args ...any) *Event {
	return write(ctx, EventStatusDone, true, args)
}

// PrintDonef is like Donef but also enforces that the plain writer will print the output. The
// plain writer normally skips Working and Done events.
func PrintDonef(ctx context.Context, format string, args ...any) *Event {
	return writef(ctx, EventStatusDone, true, format, args)
}

func Info(ctx context.Context, args ...any) *Event {
	return write(ctx, EventStatusInfo, false, args)
}

func Infof(ctx context.Context, format string, args ...any) *Event {
	return writef(ctx, EventStatusInfo, false, format, args)
}

func Error(ctx context.Context, args ...any) *Event {
	return write(ctx, EventStatusError, false, args)
}

func Errorf(ctx context.Context, format string, args ...any) *Event {
	return writef(ctx, EventStatusError, false, format, args)
}

func Warning(ctx context.Context, args ...any) *Event {
	return write(ctx, EventStatusWarning, false, args)
}

func Warningf(ctx context.Context, format string, args ...any) *Event {
	return writef(ctx, EventStatusWarning, false, format, args)
}

func write(ctx context.Context, status EventStatus, plain bool, args []any) *Event {
	ev := NewEvent(EventId(ctx), status, fmt.Sprint(args...))
	ev.plainAlways = plain
	ContextWriter(ctx).Write(ev)
	return ev
}

func writef(ctx context.Context, status EventStatus, plain bool, format string, args []any) *Event {
	ev := NewEvent(EventId(ctx), status, fmt.Sprintf(format, args...))
	ev.plainAlways = plain
	ContextWriter(ctx).Write(ev)
	return ev
}

func Write(ctx context.Context, events ...*Event) {
	ContextWriter(ctx).Write(events...)
}

func MaybeWriteError(ctx context.Context, err error) error {
	if err != nil && errcat.GetCategory(err) != errcat.Silent {
		Error(ctx, err.Error())
		err = errcat.Silent.New(err)
	}
	return err
}

type Mode string

const (
	// ModeAuto detect console capabilities.
	ModeAuto = Mode("auto")
	// ModeTTY use terminal capability for advanced rendering.
	ModeTTY = Mode("tty")
	// ModePlain dump raw events to output.
	ModePlain = Mode("plain")
	// ModeQuiet don't display events.
	ModeQuiet = Mode("quiet")
	// ModeJSON outputs a machine-readable JSON stream.
	ModeJSON = Mode("json")
)

// NewWriter returns a new multi-progress writer.
func NewWriter(out, err io.Writer, mode Mode) Writer {
	_, isTerminal := term.GetFdInfo(out)
	if mode == ModeQuiet {
		return quiet{}
	}

	tty := mode == ModeTTY
	if mode == ModeAuto && isTerminal {
		tty = true
	}
	if tty {
		return newTTYWriter(err)
	}
	if mode == ModeJSON {
		return &jsonWriter{
			out: out,
		}
	}
	return plainWriter{
		out: out,
		err: err,
	}
}
