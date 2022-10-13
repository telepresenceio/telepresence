package log

import (
	"context"
	"sync"
	"time"
)

// LevelSetter is a function that sets the log-level.
type LevelSetter func(ctx context.Context, logLevel string)

// TimedLevel is an object capable of setting a log-level for a given time
// period and then resetting it to a default.
type TimedLevel interface {
	sync.Locker

	// Get returns the current level and the time left until that level
	// is reset to default. An empty string and zero is returned if
	// no level has been set or if it has expired already.
	Get() (string, time.Duration)

	// Set sets a new log-level that will be active for the given duration. If the
	// duration is zero, then the log-level will be active until the next call to
	// Set. If level is the empty string, then duration is ignored and the log-level
	// will be reset to default.
	Set(ctx context.Context, level string, duration time.Duration)

	// Reset restores the log-level to its default value
	Reset(ctx context.Context)
}

type timedLevel struct {
	sync.Mutex
	setter       LevelSetter
	tempLevel    string
	defaultLevel string
	timer        *time.Timer
	expires      *time.Time
}

// NewTimedLevel returns a new TimedLevel for the given default level and setter.
func NewTimedLevel(defaultLevel string, setter LevelSetter) TimedLevel {
	return &timedLevel{
		setter:       setter,
		defaultLevel: defaultLevel,
	}
}

func (tl *timedLevel) Get() (string, time.Duration) {
	tl.Lock()
	defer tl.Unlock()
	if tl.tempLevel == "" || tl.expires == nil {
		return tl.tempLevel, 0
	}
	remain := time.Until(*tl.expires)
	if remain <= 0 {
		return "", 0
	}
	return tl.tempLevel, remain
}

func (tl *timedLevel) Set(ctx context.Context, level string, duration time.Duration) {
	if level == "" {
		tl.Reset(ctx)
		return
	}

	tl.Lock()
	defer tl.Unlock()

	if tl.timer != nil {
		tl.timer.Stop()
	}

	tl.setter(ctx, level)
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
	tl.tempLevel = ""
	tl.setter(ctx, tl.defaultLevel)
}
