package main

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/stretchr/testify/require"
)

var (
	CLOSE interface{} = &struct{}{}
)

func panics(f func()) (success bool) {
	defer func() {
		if e := recover(); e != nil {
			success = true
		}
	}()

	f()
	return
}

func Eventually(t *testing.T, timeoutAfter time.Duration, f func()) {
	timeout := time.After(timeoutAfter)
	tick := time.Tick(100 * time.Millisecond)

	for {
		select {
		case <-timeout:
			t.Fatal("timed out")
		case <-tick:
			if !panics(f) {
				return
			}
		}
	}
}

type Timeout time.Duration

func expect(t *testing.T, ch interface{}, values ...interface{}) {
	rch := reflect.ValueOf(ch)

	for idx, expected := range values {
		var timeoutExpected bool
		var timer *time.Timer
		switch exp := expected.(type) {
		case Timeout:
			timeoutExpected = true
			timer = time.NewTimer(time.Duration(exp))
		default:
			timeoutExpected = false
			timer = time.NewTimer(10 * time.Second)
		}

		chosen, value, ok := reflect.Select([]reflect.SelectCase{
			{
				Dir:  reflect.SelectRecv,
				Chan: rch,
			},
			{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(timer.C),
			},
		})

		if timeoutExpected && chosen != 1 {
			if ok {
				panic(fmt.Sprintf("expected timeout, got %v", value.Interface()))
			} else {
				panic("expected timeout, got CLOSE")
			}
		}

		if timeoutExpected && chosen == 1 {
			continue
		}

		if ok {
			switch exp := expected.(type) {
			case func(interface{}) bool:
				if !exp(value) {
					panic(fmt.Sprintf("predicate %d failed value %v", idx, value))
				}
			case func(string) bool:
				if !exp(value.String()) {
					panic(fmt.Sprintf("predicate %d failed value %v", idx, value))
				}
			case func([]k8s.Resource) bool:
				val, ok := value.Interface().([]k8s.Resource)
				if !ok {
					panic(fmt.Sprintf("expected a []k8s.Resource, got %v", value.Type()))
				}
				if !exp(val) {
					panic(fmt.Sprintf("predicate %d failed value %v", idx, value))
				}
			default:
				require.Equal(t, expected, value.Interface(), "read unexpected value from channel")
			}
		} else if expected != CLOSE {
			panic(fmt.Sprintf("expected %v, got CLOSE", expected))
		}
	}
}
