package auth_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func authenticatedStatus(name, uid string, groups ...string) *authnv1.TokenReviewStatus {
	return &authnv1.TokenReviewStatus{
		Authenticated: true,
		User: authnv1.UserInfo{
			Username: name,
			UID:      uid,
			Groups:   groups,
		},
	}
}

func TestAuthenticate_ValidBoundToken(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, func(token string, audiences []string) *authnv1.TokenReviewStatus {
		if token != "good-token" {
			return &authnv1.TokenReviewStatus{Authenticated: false}
		}
		status := authenticatedStatus("system:serviceaccount:demo:my-sa", "1234", "system:serviceaccounts", "system:serviceaccounts:demo")
		status.User.Extra = map[string]authnv1.ExtraValue{
			"authentication.kubernetes.io/pod-name": {"my-pod"},
			"authentication.kubernetes.io/pod-uid":  {"pod-uid-1"},
		}
		return status
	})

	a := auth.NewAuthenticator(ci)
	p, err := a.Authenticate(context.Background(), "good-token")
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "system:serviceaccount:demo:my-sa", p.Username)
	assert.Equal(t, "1234", p.UID)
	assert.Equal(t, []string{"system:serviceaccounts", "system:serviceaccounts:demo"}, p.Groups)
	assert.Equal(t, "my-pod", p.PodName)
	assert.Equal(t, "pod-uid-1", p.PodUID)
}

func TestAuthenticate_AudienceFallback(t *testing.T) {
	var firstAudiences []string
	var calls int

	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, func(token string, audiences []string) *authnv1.TokenReviewStatus {
		if calls == 0 {
			firstAudiences = audiences
		}
		calls++
		switch {
		case token == "manager-token" && len(audiences) == 1 && audiences[0] == agentconfig.ManagerTokenAudience:
			return authenticatedStatus("manager-sa", "u1")
		case token == "user-token" && len(audiences) == 0:
			return authenticatedStatus("some-user", "u2")
		default:
			return &authnv1.TokenReviewStatus{Authenticated: false}
		}
	})

	a := auth.NewAuthenticator(ci)

	p, err := a.Authenticate(context.Background(), "manager-token")
	require.NoError(t, err)
	assert.Equal(t, "manager-sa", p.Username)
	assert.Equal(t, []string{agentconfig.ManagerTokenAudience}, firstAudiences)

	p, err = a.Authenticate(context.Background(), "user-token")
	require.NoError(t, err)
	assert.Equal(t, "some-user", p.Username)
}

func TestAuthenticate_InvalidToken(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, nil)

	a := auth.NewAuthenticator(ci)
	p, err := a.Authenticate(context.Background(), "bad-token")
	assert.Nil(t, p)
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestAuthenticate_InfrastructureError(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, nil)
	failure := errors.New("connection refused")
	ci.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, failure
	})

	a := auth.NewAuthenticator(ci)
	p, err := a.Authenticate(context.Background(), "any-token")
	assert.Nil(t, p)
	assert.False(t, errors.Is(err, auth.ErrInvalidToken))
	require.Error(t, err)
}

func TestAuthenticate_Caching(t *testing.T) {
	var calls atomic.Int32

	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, func(token string, audiences []string) *authnv1.TokenReviewStatus {
		calls.Add(1)
		if token == "good" && len(audiences) == 1 && audiences[0] == agentconfig.ManagerTokenAudience {
			return authenticatedStatus("u", "1")
		}
		return &authnv1.TokenReviewStatus{Authenticated: false}
	})

	a := auth.NewAuthenticator(ci)

	_, err := a.Authenticate(context.Background(), "good")
	require.NoError(t, err)
	afterFirst := calls.Load()
	assert.Equal(t, int32(1), afterFirst)

	_, err = a.Authenticate(context.Background(), "good")
	require.NoError(t, err)
	assert.Equal(t, afterFirst, calls.Load())

	_, err = a.Authenticate(context.Background(), "bad")
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
	afterBad := calls.Load()
	assert.Greater(t, afterBad, afterFirst)

	_, err = a.Authenticate(context.Background(), "bad")
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
	assert.Equal(t, afterBad, calls.Load())
}
