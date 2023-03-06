package authenticator

import (
	"context"
	"fmt"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	clientcmd_api "k8s.io/client-go/tools/clientcmd/api"

	mock_authenticator "github.com/telepresenceio/telepresence/v2/pkg/authenticator/mocks"
)

const mockExecCredentials = `{
    "kind": "ExecCredential",
    "apiVersion": "client.authentication.k8s.io/v1beta1",
    "spec": {
        "interactive": false
    },
    "status": {
        "expirationTimestamp": "2023-03-01T19:43:26Z",
        "token": "xxxx"
    }
}`

type SuiteService struct {
	suite.Suite

	ctrl *gomock.Controller

	kubeClientConfig        *mock_authenticator.MockClientConfig
	execCredentialsResolver *mock_authenticator.MockExecCredentialsResolver

	service *Service
}

func (s *SuiteService) SetupTest() {
	s.ctrl = gomock.NewController(s.T())

	s.kubeClientConfig = mock_authenticator.NewMockClientConfig(s.ctrl)
	s.execCredentialsResolver = mock_authenticator.NewMockExecCredentialsResolver(s.ctrl)

	s.service = &Service{
		kubeClientConfig:        s.kubeClientConfig,
		execCredentialsResolver: s.execCredentialsResolver,
	}
}

func (s *SuiteService) AfterTest() {
	s.ctrl.Finish()
}

func (s *SuiteService) TestGetExecCredentialsGetRawConfigFail() {
	// given
	ctx := context.Background()
	s.kubeClientConfig.EXPECT().RawConfig().Return(clientcmd_api.Config{}, fmt.Errorf("some internal error"))

	// then
	rawCredentials, err := s.service.GetExecCredentials(ctx, "context")

	// when
	assert.ErrorContains(s.T(), err, "some internal error")
	assert.Empty(s.T(), rawCredentials)
}

func (s *SuiteService) TestGetExecCredentialsContextNotFound() {
	// given
	ctx := context.Background()
	kubeConfig := clientcmd_api.Config{
		AuthInfos: map[string]*clientcmd_api.AuthInfo{
			"my-user": {
				Exec: &clientcmd_api.ExecConfig{},
			},
		},
		Contexts: map[string]*clientcmd_api.Context{
			"my-context": {},
		},
		CurrentContext: "my-context",
	}
	s.kubeClientConfig.EXPECT().RawConfig().Return(kubeConfig, nil)

	// then
	rawCredentials, err := s.service.GetExecCredentials(ctx, "unknown-context")

	// when
	assert.ErrorContains(s.T(), err, "failed to get exec config from context unknown-context, kube context unknown-context doesn't exist")
	assert.Empty(s.T(), rawCredentials)
}

func (s *SuiteService) TestGetExecCredentialsAuthInfoNotFound() {
	// given
	ctx := context.Background()
	kubeConfig := clientcmd_api.Config{
		Clusters: map[string]*clientcmd_api.Cluster{
			"my-cluster": {},
		},
		AuthInfos: map[string]*clientcmd_api.AuthInfo{
			"my-user": {},
		},
		Contexts: map[string]*clientcmd_api.Context{
			"my-context": {
				Cluster:  "my-cluster",
				AuthInfo: "my-unknown-user",
			},
		},
		CurrentContext: "my-context",
	}
	s.kubeClientConfig.EXPECT().RawConfig().Return(kubeConfig, nil)

	// then
	rawCredentials, err := s.service.GetExecCredentials(ctx, "my-context")

	// when
	assert.ErrorContains(s.T(), err, "failed to get exec config from context my-context, auth info my-unknown-user doesn't exist")
	assert.Nil(s.T(), rawCredentials)
}

func (s *SuiteService) TestGetExecCredentialsAuthInfoNotExecType() {
	// given
	ctx := context.Background()
	kubeConfig := clientcmd_api.Config{
		Clusters: map[string]*clientcmd_api.Cluster{
			"my-cluster": {},
		},
		AuthInfos: map[string]*clientcmd_api.AuthInfo{
			"my-user": {},
		},
		Contexts: map[string]*clientcmd_api.Context{
			"my-context": {
				Cluster:  "unknown-cluster",
				AuthInfo: "my-user",
			},
		},
		CurrentContext: "my-context",
	}
	s.kubeClientConfig.EXPECT().RawConfig().Return(kubeConfig, nil)

	// then
	rawCredentials, err := s.service.GetExecCredentials(ctx, "my-context")

	// when
	assert.ErrorContains(s.T(), err, "failed to get exec config from context my-context, auth info my-user isn't of type exec")
	assert.Nil(s.T(), rawCredentials)
}

var mockKubeConfig = clientcmd_api.Config{
	Clusters: map[string]*clientcmd_api.Cluster{
		"my-cluster": {},
	},
	AuthInfos: map[string]*clientcmd_api.AuthInfo{
		"my-user": {
			Exec: &clientcmd_api.ExecConfig{
				Command: "/opt/homebrew/Caskroom/google-cloud-sdk/latest/google-cloud-sdk/bin/gke-gcloud-auth-plugin",
				Args:    []string{"jdoe@gmail.com"},
				Env: []clientcmd_api.ExecEnvVar{
					{
						Name:  "SOME_VAR",
						Value: "SOME_VALUE",
					},
				},
			},
		},
	},
	Contexts: map[string]*clientcmd_api.Context{
		"my-context": {
			Cluster:  "my-cluster",
			AuthInfo: "my-user",
		},
	},
	CurrentContext: "my-context",
}

func (s *SuiteService) TestGetExecCredentialsResolveFailure() {
	// given
	ctx := context.Background()
	s.kubeClientConfig.EXPECT().RawConfig().Return(mockKubeConfig, nil)
	s.execCredentialsResolver.EXPECT().Resolve(ctx, &clientcmd_api.ExecConfig{
		Command: mockKubeConfig.AuthInfos["my-user"].Exec.Command,
		Args:    mockKubeConfig.AuthInfos["my-user"].Exec.Args,
		Env:     mockKubeConfig.AuthInfos["my-user"].Exec.Env,
	}).Return(nil, fmt.Errorf("some internal error"))

	// then
	rawCredentials, err := s.service.GetExecCredentials(ctx, "my-context")

	// when
	assert.ErrorContains(s.T(), err, "some internal error")
	assert.Nil(s.T(), rawCredentials)
}

func (s *SuiteService) TestGetExecCredentialsSuccess() {
	// given
	ctx := context.Background()
	s.kubeClientConfig.EXPECT().RawConfig().Return(mockKubeConfig, nil)
	s.execCredentialsResolver.EXPECT().Resolve(ctx, &clientcmd_api.ExecConfig{
		Command: mockKubeConfig.AuthInfos["my-user"].Exec.Command,
		Args:    mockKubeConfig.AuthInfos["my-user"].Exec.Args,
		Env:     mockKubeConfig.AuthInfos["my-user"].Exec.Env,
	}).Return([]byte(mockExecCredentials), nil)

	// then
	rawCredentials, err := s.service.GetExecCredentials(ctx, "my-context")

	// when
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), []byte(mockExecCredentials), rawCredentials)
}

func TestSuiteService(t *testing.T) {
	suite.Run(t, new(SuiteService))
}
