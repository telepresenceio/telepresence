package client

import (
	"context"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	"github.com/pkg/errors"
)

const defaultRetryDelay = 100 * time.Millisecond
const defaultMaxDelay = 3 * time.Second

func Retry(c context.Context, f func(context.Context) error, durations ...time.Duration) error {
	delay := defaultRetryDelay
	maxDelay := defaultMaxDelay
	maxTime := time.Duration(0)

	switch len(durations) {
	case 3:
		maxTime = durations[2]
		fallthrough
	case 2:
		maxDelay = durations[1]
		if maxDelay == 0 {
			maxDelay = defaultMaxDelay
		}
		fallthrough
	case 1:
		delay = durations[0]
		if delay == 0 {
			delay = defaultRetryDelay
		}
	}

	if maxDelay < delay {
		maxDelay = delay
	}
	var endTime <-chan time.Time
	if maxTime > 0 {
		endTime = time.After(maxTime)
	} else {
		endTime = make(chan time.Time)
	}

	r := make(chan error)
	for {
		fatal := false
		go func() {
			defer func() {
				if err := dutil.PanicToError(recover()); err != nil {
					fatal = true
					r <- err
				}
			}()
			r <- f(c)
		}()
		select {
		case err := <-r:
			if fatal || err == nil {
				return err
			}
			// Logging at higher log levels should be done in the called function
			dlog.Trace(c, err.Error())
		case <-c.Done():
			return nil
		case <-endTime:
			return errors.New("retry timed out")
		}
		time.Sleep(delay)
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}
