package limiter

import "time"

// A limiter can be used to rate limit and/or coalesce a series of
// time-based events. This just captures the logic of deciding whether
// an event occuring at a specific time T should be ignored, acted
// upon, or delayed.
type Limiter interface {
	// The Limit() method works kinda like an 8 ball. You pass it
	// the time of an event, and it returns one of Yes, No, or
	// "Later". A zero return value means "Yes", the event should
	// be acted upon right away. A negative return value means
	// "No", the event should be dropped entirely, and a positive
	// return value means to check back within the returned
	// interval.
	//
	// The order that this is invoked is important, and it is
	// expected that this is invoked with a monotonically
	// increasing set of timestamps. Typically this will be
	// invoked when an event is generated and will be the result
	// of time.Now(), but for testing or other cases, this could
	// be invoked with any set of historic or future timestamps so
	// long as they are invoked in monotonically increasing order.
	Limit(now time.Time) time.Duration
}

type limiter struct {
	interval   time.Duration
	lastAction time.Time
}

// Constructs a new limiter that will coalesce any events occuring
// within the specified interval.
func NewIntervalLimiter(interval time.Duration) Limiter {
	return &limiter{
		interval: interval,
	}
}

func (l *limiter) Limit(now time.Time) time.Duration {
	since := now.Sub(l.lastAction)
	if since >= l.interval {
		l.lastAction = now
		return 0
	} else {
		return l.interval - since
	}
}
