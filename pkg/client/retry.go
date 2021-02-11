package client

import (
	"context"
	"fmt"
	"time"

	"github.com/datawire/dlib/dlog"
)

const defaultRetryDelay = 100 * time.Millisecond
const defaultMaxDelay = 3 * time.Second

// Retry will run the given function repeatedly with an increasing delay until it returns without error.
//
// The function takes 0 to 3 durations with the following meaning
//  Delay - initial delay, i.e. the delay between the first and the second call.
//  MaxDelay - maximum delay between calling the functions (delay will never grow beyond this value)
//  MaxTime - maximum time before giving up.
func Retry(c context.Context, text string, f func(context.Context) error, durations ...time.Duration) error {
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

	if maxTime > 0 {
		var cancel context.CancelFunc
		c, cancel = context.WithTimeout(c, maxTime)
		defer cancel()
	}

	for {
		err := f(c)
		if err == nil {
			// success
			return nil
		}

		// Logging at higher log levels should be done in the called function
		dlog.Debugf(c, "%s waiting %s before retrying after error: %v", text, delay.String(), err)

		select {
		case <-c.Done():
			if c.Err() == context.DeadlineExceeded {
				err = fmt.Errorf("retry timed out after: %s", maxTime.String())
			}
			return err
		case <-time.After(delay):
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}
