package mutator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	admission "k8s.io/api/admission/v1"

	"github.com/stretchr/testify/assert"
)

// fakeAgentInjector is a minimal AgentInjector whose Uninstall records that
// it was called, so uninstallHandler tests can assert it ran without
// exercising the real agentInjector's Map/rollout machinery.
type fakeAgentInjector struct {
	uninstalled bool
}

func (f *fakeAgentInjector) Inject(context.Context, *admission.AdmissionRequest) (PatchOps, error) {
	return nil, nil
}

func (f *fakeAgentInjector) Uninstall(context.Context) {
	f.uninstalled = true
}

// TestUninstallHandler_RunsInjectorAndReap verifies that a DELETE /uninstall
// request runs both the agent injector's sidecar rollback and the
// node-agent Job reap callback, and reports success even though nothing
// about the reap's outcome is checked here (that requires a non-nil error).
func TestUninstallHandler_RunsInjectorAndReap(t *testing.T) {
	t.Parallel()

	ai := &fakeAgentInjector{}
	reapCalled := false
	reap := func(context.Context) error {
		reapCalled = true
		return nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/uninstall", nil)
	uninstallHandler(ai, reap)(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, ai.uninstalled, "injector Uninstall must run")
	assert.True(t, reapCalled, "node-agent reap callback must run")
}

// TestUninstallHandler_ReapErrorStillSucceeds verifies that a failing reap
// callback is logged (implicitly, by not being propagated) rather than
// turning the request into a failure -- the pre-delete hook is best-effort
// and a Job reap failure must not block the sidecar rollback the injector
// already performed.
func TestUninstallHandler_ReapErrorStillSucceeds(t *testing.T) {
	t.Parallel()

	ai := &fakeAgentInjector{}
	reap := func(context.Context) error {
		return errors.New("boom")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/uninstall", nil)
	uninstallHandler(ai, reap)(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, ai.uninstalled)
}

// TestUninstallHandler_NilInjector verifies that the handler tolerates a
// nil AgentInjector -- the node-agent-only, injector-disabled case served
// by ServeNodeAgentUninstall -- and still runs the reap callback.
func TestUninstallHandler_NilInjector(t *testing.T) {
	t.Parallel()

	reapCalled := false
	reap := func(context.Context) error {
		reapCalled = true
		return nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/uninstall", nil)
	uninstallHandler(nil, reap)(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, reapCalled)
}

// TestUninstallHandler_NilReap verifies that the handler tolerates a nil
// reap callback -- node-agent mode disabled -- and still runs the
// injector's Uninstall.
func TestUninstallHandler_NilReap(t *testing.T) {
	t.Parallel()

	ai := &fakeAgentInjector{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/uninstall", nil)
	uninstallHandler(ai, nil)(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, ai.uninstalled)
}

// TestUninstallHandler_WrongMethod verifies that a non-DELETE request is
// rejected before either the injector or the reap callback runs.
func TestUninstallHandler_WrongMethod(t *testing.T) {
	t.Parallel()

	ai := &fakeAgentInjector{}
	reapCalled := false
	reap := func(context.Context) error {
		reapCalled = true
		return nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/uninstall", nil)
	uninstallHandler(ai, reap)(w, r)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.False(t, ai.uninstalled)
	assert.False(t, reapCalled)
}
