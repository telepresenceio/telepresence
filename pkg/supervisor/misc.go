package supervisor

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"time"
)

// Logger is what Supervisor may use as a logging backend.
type Logger interface {
	Printf(format string, v ...interface{})
}

// DefaultLogger is a logger that uses the stdlib "log" package
// default logger.
type DefaultLogger struct{}

// Printf implements the Logger interface.
func (d *DefaultLogger) Printf(format string, v ...interface{}) {
	log.Printf(format, v...)
}

// Run creates a single-purpose Supervisor and runs a worker function
// with it.
func Run(name string, f func(*Process) error) []error {
	sup := WithContext(context.Background())
	sup.Supervise(&Worker{Name: name, Work: f})
	return sup.Run()
}

// MustRun is like Run, but panics if there are errors.
func MustRun(name string, f func(*Process) error) {
	errs := Run(name, f)
	if len(errs) > 0 {
		panic(fmt.Sprintf("%s: %v", name, errs))
	}
}

func nextDelay(delay time.Duration) time.Duration {
	switch {
	case delay <= 0:
		return 100 * time.Millisecond
	case delay < 3*time.Second:
		return delay * 2
	default:
		return 3 * time.Second
	}
}

// WorkFunc creates a work function from a function whose signature
// includes a process plus additional arguments.
func WorkFunc(fn interface{}, args ...interface{}) func(*Process) error {
	fnv := reflect.ValueOf(fn)
	return func(p *Process) error {
		vargs := []reflect.Value{reflect.ValueOf(p)}
		for _, a := range args {
			vargs = append(vargs, reflect.ValueOf(a))
		}
		result := fnv.Call(vargs)
		if len(result) != 1 {
			panic(fmt.Sprintf("unexpected result: %v", result))
		}
		v := result[0].Interface()
		if v != nil {
			err, ok := v.(error)
			if !ok {
				panic(fmt.Sprintf("unrecognized result type: %v", v))
			}
			return err
		}
		return nil
	}
}
