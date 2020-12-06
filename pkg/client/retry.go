package client

import (
	"context"
	"fmt"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
)

const defaultRetryDelay = 100 * time.Millisecond
const defaultMaxDelay = 3 * time.Second

// Retry will run the given function repeatedly with an increasing delay until it returns without error.
//
// The function takes 0 to 3 durations with the following meaning
//  Delay - initial delay, i.e. the delay between the first and the second call.
//  MaxDelay - maximum delay between calling the functions (delay will never grow beyond this value)
//  MaxTime - maximum time before giving up.
func Retry(c context.Context, f func(context.Context) error, durations ...time.Duration) (err error) {
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

	done := make(chan bool)
	if maxTime > 0 {
		var cancel context.CancelFunc
		c, cancel = context.WithCancel(c)
		go func() {
			select {
			case <-done:
			case <-c.Done():
			case <-time.After(maxTime):
				err = fmt.Errorf("retry timed out after: %s", maxTime.String())
				cancel()
			}
		}()
	}

	defer func() {
		if pe := dutil.PanicToError(recover()); pe != nil {
			err = pe
		}
		close(done)
	}()

	for retry := 0; ; retry++ {
		funcErr := f(c)
		if funcErr == nil {
			// success
			return nil
		}

		// Logging at higher log levels should be done in the called function
		dlog.Debugf(c, "waiting %s before retrying after error: %s", delay.String(), funcErr.Error())

		select {
		case <-c.Done():
			return err
		case <-time.After(delay):
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}
