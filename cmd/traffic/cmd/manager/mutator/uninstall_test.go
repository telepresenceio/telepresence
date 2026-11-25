package mutator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	admission "k8s.io/api/admission/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const hookToken = "hook-token"

// acceptHookToken accepts only hookToken.
func acceptHookToken(_ context.Context, token string) error {
	if token != hookToken {
		return errors.New("wrong token")
	}
	return nil
}

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
	r.Header.Set("Authorization", "Bearer "+hookToken)
	uninstallHandler(context.Background(), acceptHookToken, ai, reap)(w, r)

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
	r.Header.Set("Authorization", "Bearer "+hookToken)
	uninstallHandler(context.Background(), acceptHookToken, ai, reap)(w, r)

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
	r.Header.Set("Authorization", "Bearer "+hookToken)
	uninstallHandler(context.Background(), acceptHookToken, nil, reap)(w, r)

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
	r.Header.Set("Authorization", "Bearer "+hookToken)
	uninstallHandler(context.Background(), acceptHookToken, ai, nil)(w, r)

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
	r.Header.Set("Authorization", "Bearer "+hookToken)
	uninstallHandler(context.Background(), acceptHookToken, ai, reap)(w, r)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.False(t, ai.uninstalled)
	assert.False(t, reapCalled)
}

// TestUninstallHandler_Unauthorized verifies that a request without a valid bearer token gets 401
// and runs neither the injector's Uninstall nor the reap callback.
func TestUninstallHandler_Unauthorized(t *testing.T) {
	t.Parallel()

	for name, header := range map[string]string{
		"none":       "",
		"not bearer": "Basic " + hookToken,
		"empty":      "Bearer ",
		"other":      "Bearer other-token",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ai := &fakeAgentInjector{}
			reapCalled := false
			reap := func(context.Context) error {
				reapCalled = true
				return nil
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodDelete, "/uninstall", nil)
			if header != "" {
				r.Header.Set("Authorization", header)
			}
			uninstallHandler(context.Background(), acceptHookToken, ai, reap)(w, r)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.False(t, ai.uninstalled)
			assert.False(t, reapCalled)
		})
	}
}

// TestUninstallHandler_NoAuthenticator verifies that a handler without an authenticator refuses
// every request.
func TestUninstallHandler_NoAuthenticator(t *testing.T) {
	t.Parallel()

	ai := &fakeAgentInjector{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/uninstall", nil)
	r.Header.Set("Authorization", "Bearer "+hookToken)
	uninstallHandler(context.Background(), nil, ai, nil)(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.False(t, ai.uninstalled)
}

// TestServeMutatingFunc_OversizedBody verifies that a request body above the limit is refused
// before the mutating function runs.
func TestServeMutatingFunc_OversizedBody(t *testing.T) {
	t.Parallel()

	called := false
	mf := func(context.Context, *admission.AdmissionRequest) (PatchOps, error) {
		called = true
		return nil, nil
	}
	body := strings.NewReader(`{"request":{"uid":"` + strings.Repeat("a", maxRequestBodySize) + `"}}`)
	r := httptest.NewRequest(http.MethodPost, "/traffic-agent", body)
	r.Header.Set("Content-Type", jsonContentType)
	_, code, err := serveMutatingFunc(context.Background(), httptest.NewRecorder(), r, mf)

	require.Error(t, err)
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.False(t, called)
}
