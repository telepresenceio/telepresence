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

func expect(t *testing.T, ch interface{}, values ...interface{}) {
	timer := time.NewTimer(10 * time.Second)
	rch := reflect.ValueOf(ch)

	for idx, expected := range values {
		chosen, value, ok := reflect.Select([]reflect.SelectCase{{
			Dir:  reflect.SelectRecv,
			Chan: rch,
		},
			{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(timer.C),
			}})

		if chosen == 1 {
			panic("timed out")
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
			panic(fmt.Sprintf("expected CLOSE, got %v", value))
		}
	}
}
