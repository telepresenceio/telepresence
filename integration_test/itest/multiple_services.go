package itest

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

type MultipleServices interface {
	NamespacePair
	Name() string
	ServiceCount() int
}

type multipleServices struct {
	NamespacePair
	name         string
	serviceCount int
}

func WithMultipleServices(np NamespacePair, name string, serviceCount int, f func(MultipleServices)) {
	np.HarnessT().Run(fmt.Sprintf("Test_Services_%d", serviceCount), func(t *testing.T) {
		ctx := WithT(np.HarnessContext(), t)
		ms := &multipleServices{NamespacePair: np, name: name, serviceCount: serviceCount}
		ms.PushHarness(ctx, ms.setup, ms.tearDown)
		defer ms.PopHarness()
		f(ms)
	})
}

func (h *multipleServices) setup(ctx context.Context) bool {
	wg := sync.WaitGroup{}
	wg.Add(h.serviceCount)
	for i := 0; i < h.serviceCount; i++ {
		go func(i int) {
			defer wg.Done()
			h.ApplyEchoService(ctx, fmt.Sprintf("%s-%d", h.name, i), 80)
		}(i)
	}
	wg.Wait()
	return true
}

func (h *multipleServices) tearDown(ctx context.Context) {
	wg := sync.WaitGroup{}
	wg.Add(h.serviceCount)
	for i := 0; i < h.serviceCount; i++ {
		go func(i int) {
			defer wg.Done()
			h.DeleteSvcAndWorkload(ctx, "deploy", fmt.Sprintf("hello-%d", i))
		}(i)
	}
	wg.Wait()
}

func (h *multipleServices) Name() string {
	return h.name
}

func (h *multipleServices) ServiceCount() int {
	return h.serviceCount
}
