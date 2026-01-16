package log

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/telepresenceio/clog"
)

const UnsetLevel = slog.Level(math.MaxInt)

// LevelSetter is a function that sets the log-level.
type LevelSetter func(ctx context.Context, logLevel slog.Level) bool

// TimedLevel is an object capable of setting a log-level for a given time
// period and then resetting it to a default.
type TimedLevel interface {
	sync.Locker

	// Get returns the current level and the time left until that level
	// is reset to default. An empty string and zero is returned if
	// no level has been set or if it has expired already.
	Get() (slog.Level, time.Duration)

	// Set sets a new log-level that will be active for the given duration. If the
	// duration is zero, then the log-level will be active until the next call to
	// Set. If level is the empty string, then duration is ignored and the log-level
	// will be reset to default.
	Set(ctx context.Context, level slog.Level, duration time.Duration)

	// Reset restores the log-level to its default value
	Reset(ctx context.Context)
}

type timedLevel struct {
	sync.Mutex
	setter       LevelSetter
	tempLevel    slog.Level
	defaultLevel slog.Level
	timer        *time.Timer
	expires      *time.Time
}

// NewTimedLevel returns a new TimedLevel for the given default level and setter.
func NewTimedLevel(defaultLevel slog.Level, setter LevelSetter) TimedLevel {
	return &timedLevel{
		setter:       setter,
		tempLevel:    UnsetLevel,
		defaultLevel: defaultLevel,
	}
}

func (tl *timedLevel) Get() (slog.Level, time.Duration) {
	tl.Lock()
	defer tl.Unlock()
	if tl.tempLevel == UnsetLevel || tl.expires == nil {
		return tl.tempLevel, 0
	}
	remain := time.Until(*tl.expires)
	if remain <= 0 {
		return UnsetLevel, 0
	}
	return tl.tempLevel, remain
}

func (tl *timedLevel) Set(ctx context.Context, level slog.Level, duration time.Duration) {
	if level == UnsetLevel {
		tl.Reset(ctx)
		return
	}

	tl.Lock()
	defer tl.Unlock()

	if tl.timer != nil {
		tl.timer.Stop()
	}

	if tl.setter(ctx, level) {
		clog.Infof(ctx, "Logging at this level %q", clog.LevelWithTrace(level))
	}
	tl.tempLevel = level
	if duration == 0 {
		tl.expires = nil
		tl.timer = nil
		return
	}

	exTime := time.Now().Add(duration)
	tl.expires = &exTime
	if tl.timer == nil {
		tl.timer = time.AfterFunc(duration, func() {
			tl.Reset(ctx)
		})
	} else {
		tl.timer.Reset(duration)
	}
}

// Reset restores the log-level to its default value.
func (tl *timedLevel) Reset(ctx context.Context) {
	tl.Lock()
	defer tl.Unlock()
	tl.expires = nil
	tl.tempLevel = UnsetLevel
	tl.setter(ctx, tl.defaultLevel)
}
