package itest

import (
	"context"
	"regexp"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

type Harness interface {
	Cluster

	PushHarness(ctx context.Context, setup func(ctx context.Context) bool, tearDown func(ctx context.Context))
	RunSuite(TestingSuite)

	HarnessContext() context.Context
	SetupSuite()
	HarnessT() *testing.T
	PopHarness()
}

type upAndDown struct {
	setup     func(ctx context.Context) bool
	tearDown  func(ctx context.Context)
	setupWith context.Context
	wasSetup  bool
}

type harness struct {
	Cluster
	ctx        context.Context
	upAndDowns []upAndDown
	wasSetup   bool
}

func NewContextHarness(ctx context.Context) Harness {
	return &harness{Cluster: GetGlobalHarness(ctx), ctx: ctx}
}

func (h *harness) PushHarness(ctx context.Context, setup func(ctx context.Context) bool, tearDown func(ctx context.Context)) {
	h.upAndDowns = append(h.upAndDowns, upAndDown{setup: setup, tearDown: tearDown, setupWith: ctx})
	h.wasSetup = false
}

func (h *harness) HarnessContext() context.Context {
	if l := len(h.upAndDowns) - 1; l >= 0 {
		return h.upAndDowns[l].setupWith
	}
	return h.ctx
}

func (h *harness) RunSuite(s TestingSuite) {
	if suiteEnabled(h.HarnessContext(), s) {
		s.setContext(s.AmendSuiteContext(h.HarnessContext()))
		h.HarnessT().Run(s.SuiteName(), func(t *testing.T) { suite.Run(t, s) })
	}
}

func suiteEnabled(ctx context.Context, s TestingSuite) bool {
	suiteRx := dos.Getenv(ctx, "TEST_SUITE")
	if suiteRx == "" {
		return true
	}
	r, err := regexp.Compile(suiteRx)
	if err != nil {
		getT(ctx).Fatal(err)
	}
	return r.MatchString(s.SuiteName())
}

// SetupSuite calls all functions that has been added with AddSetup in the order they
// were added. This only happens once. Repeated calls to this function on the same
// instance will do nothing.
//
// Repeated calls are expected since this function will be called before each
// test.
func (h *harness) SetupSuite() {
	if h.wasSetup {
		return
	}
	h.wasSetup = true
	if err := h.GeneralError(); err != nil {
		h.HarnessT().Fatal(err) // Immediately fail the test if a general error has been set
	}
	uds := h.upAndDowns
	for i := range uds {
		upDown := &uds[i]
		if setup := upDown.setup; setup != nil {
			upDown.setup = nil // Never setup twice
			if upDown.wasSetup = safeSetUp(upDown.setupWith, setup); !upDown.wasSetup {
				getT(h.ctx).FailNow()
			}
		}
	}
}

// PopHarness calls the tearDown function that was pushed last and removes it.
func (h *harness) PopHarness() {
	last := len(h.upAndDowns) - 1
	if last < 0 {
		return
	}
	upDown := &h.upAndDowns[last]
	h.upAndDowns = h.upAndDowns[:last]
	if upDown.setupWith != nil {
		if tearDown := upDown.tearDown; tearDown != nil {
			upDown.tearDown = nil // Never tear down twice
			if upDown.wasSetup {
				safeTearDown(upDown.setupWith, tearDown)
			}
		}
	}
}

func safeSetUp(ctx context.Context, f func(context.Context) bool) bool {
	defer failOnPanic(ctx)
	return f(ctx)
}

func safeTearDown(ctx context.Context, f func(context.Context)) {
	defer failOnPanic(ctx)
	f(ctx)
}

func failOnPanic(ctx context.Context) {
	if r := recover(); r != nil {
		t := getT(ctx)
		t.Errorf("test panicked: %v\n%s", r, debug.Stack())
		t.FailNow()
	}
}

func (h *harness) HarnessT() *testing.T {
	return getT(h.HarnessContext())
}
