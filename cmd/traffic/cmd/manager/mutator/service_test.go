package mutator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admission "k8s.io/api/admission/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type contextRecordingInjector struct {
	uninstallContext context.Context
}

func (*contextRecordingInjector) Inject(context.Context, *admission.AdmissionRequest) (PatchOps, error) {
	return nil, nil
}

func (i *contextRecordingInjector) Uninstall(ctx context.Context) {
	i.uninstallContext = ctx
}

func TestApplyMutatorError(t *testing.T) {
	t.Run("user error is admitted with a warning", func(t *testing.T) {
		// A User error must not deny the pod, otherwise the ReplicaSet controller retries the
		// creation indefinitely and wedges the workload's rollout.
		resp := &admission.AdmissionResponse{Allowed: true}
		err := errcat.User.Newf("invalid value %q for annotation %s", "bogus", "telepresence.getambassador.io/inject-traffic-agent")
		applyMutatorError(resp, err)
		assert.True(t, resp.Allowed, "pod creation must still be allowed for a user error")
		assert.Nil(t, resp.Result)
		assert.Equal(t, []string{err.Error()}, resp.Warnings)
	})

	t.Run("wrapped user error is still recognized", func(t *testing.T) {
		resp := &admission.AdmissionResponse{Allowed: true}
		err := fmt.Errorf("context: %w", errcat.User.New("nested user error"))
		applyMutatorError(resp, err)
		assert.True(t, resp.Allowed)
		assert.Len(t, resp.Warnings, 1)
	})

	t.Run("transient error denies so the create is retried", func(t *testing.T) {
		resp := &admission.AdmissionResponse{Allowed: true}
		err := errors.New("unable to get or generate agent config for workload echo.demo")
		applyMutatorError(resp, err)
		assert.False(t, resp.Allowed, "a transient error must deny so the pod creation is retried")
		assert.Nil(t, resp.Warnings)
		if assert.NotNil(t, resp.Result) {
			assert.Equal(t, err.Error(), resp.Result.Message)
		}
	})
}

func TestUninstallHandlerUsesManagerContext(t *testing.T) {
	type contextKey struct{}
	managerCtx := context.WithValue(context.Background(), contextKey{}, "manager")
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodDelete, "/uninstall", nil).WithContext(requestCtx)
	injector := &contextRecordingInjector{}

	recorder := httptest.NewRecorder()
	uninstallHandlerWithContext(managerCtx, injector, nil)(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotNil(t, injector.uninstallContext)
	assert.NoError(t, injector.uninstallContext.Err())
	assert.Equal(t, "manager", injector.uninstallContext.Value(contextKey{}))
}
