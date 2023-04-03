package mutator

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	mock_kubernetes "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator/mocks"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

//go:generate go run github.com/golang/mock/mockgen -package=mock_kubernetes -destination=mocks/k8s_interface_mock.go k8s.io/client-go/kubernetes Interface
//go:generate go run github.com/golang/mock/mockgen -package=mock_kubernetes -destination=mocks/k8s_corev1_mock.go k8s.io/client-go/kubernetes/typed/core/v1 CoreV1Interface
//go:generate go run github.com/golang/mock/mockgen -package=mock_kubernetes -destination=mocks/k8s_configmap_mock.go k8s.io/client-go/kubernetes/typed/core/v1 ConfigMapInterface
type suiteConfigWatcher struct {
	suite.Suite

	ctrl *gomock.Controller

	kubeApiMock      *mock_kubernetes.MockInterface
	coreV1ApiMock    *mock_kubernetes.MockCoreV1Interface
	configMapApiMock *mock_kubernetes.MockConfigMapInterface

	configWatcher *configWatcher
}

func (s *suiteConfigWatcher) SetupTest() {
	s.ctrl = gomock.NewController(s.T())

	s.kubeApiMock = mock_kubernetes.NewMockInterface(s.ctrl)
	s.coreV1ApiMock = mock_kubernetes.NewMockCoreV1Interface(s.ctrl)
	s.configMapApiMock = mock_kubernetes.NewMockConfigMapInterface(s.ctrl)

	s.kubeApiMock.EXPECT().CoreV1().Return(s.coreV1ApiMock).AnyTimes()
	s.coreV1ApiMock.EXPECT().ConfigMaps(gomock.Any()).Return(s.configMapApiMock).AnyTimes()

	s.configWatcher = &configWatcher{
		name:           "watcher",
		namespaces:     []string{"ambassador"},
		data:           make(map[string]map[string]string),
		configUpdaters: make(map[string]*configUpdater),
	}
}

func (s *suiteConfigWatcher) TestStoreForEntryAlreadySet() {
	// given
	ctx := k8sapi.WithK8sInterface(context.Background(), s.kubeApiMock)
	namespace := "my-app"
	sidecarConfig := &agentconfig.Sidecar{
		AgentName: "echo-easy",
		Namespace: namespace,
	}

	rawSidecarConfig, _ := yaml.Marshal(sidecarConfig)
	s.configWatcher.data = map[string]map[string]string{
		namespace: {
			"echo-easy": string(rawSidecarConfig),
		},
	}

	// when
	err := s.configWatcher.Store(ctx, sidecarConfig, true)

	// then
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), map[string]map[string]string{
		namespace: {
			"echo-easy": string(rawSidecarConfig),
		},
	}, s.configWatcher.data)
}

func (s *suiteConfigWatcher) TestStoreForExistingConfigMap() {
	// given
	ctx := k8sapi.WithK8sInterface(context.Background(), s.kubeApiMock)
	namespace := "my-app"
	sidecarConfig := &agentconfig.Sidecar{
		AgentName: "echo-easy",
		Namespace: namespace,
	}

	rawSidecarConfig, _ := yaml.Marshal(sidecarConfig)
	expectedData := map[string]string{
		"echo-easy":   string(rawSidecarConfig),
		"echo-easy-2": string(rawSidecarConfig),
	}

	s.configWatcher.data = map[string]map[string]string{
		namespace: {
			"echo-easy-2": string(rawSidecarConfig),
		},
	}

	s.configMapApiMock.EXPECT().Get(ctx, agentconfig.ConfigMap, meta.GetOptions{}).Return(&v1.ConfigMap{
		ObjectMeta: meta.ObjectMeta{
			Name:      agentconfig.ConfigMap,
			Namespace: namespace,
		},
		Data: nil,
	}, nil)

	s.configMapApiMock.EXPECT().Update(gomock.Any(), &v1.ConfigMap{
		ObjectMeta: meta.ObjectMeta{
			Name:      agentconfig.ConfigMap,
			Namespace: namespace,
		},
		Data: expectedData,
	}, meta.UpdateOptions{}).Return(nil, nil)

	// when
	err := s.configWatcher.Store(ctx, sidecarConfig, true)

	// then
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), map[string]map[string]string{
		namespace: expectedData,
	}, s.configWatcher.data)
}

func (s *suiteConfigWatcher) TestStoreForNewConfigMapNoSnapshotUpdate() {
	// given
	ctx := k8sapi.WithK8sInterface(context.Background(), s.kubeApiMock)
	namespace := "my-app"
	s.configWatcher.data = map[string]map[string]string{namespace: {}}
	sidecarConfig := &agentconfig.Sidecar{
		AgentName: "echo-easy",
		Namespace: namespace,
	}

	rawSidecarConfig, _ := yaml.Marshal(sidecarConfig)

	s.configMapApiMock.EXPECT().
		Get(ctx, agentconfig.ConfigMap, meta.GetOptions{}).
		Return(nil, errors.NewNotFound(v1.Resource("configmap"), agentconfig.ConfigMap))

	s.configMapApiMock.EXPECT().
		Create(gomock.Any(), &v1.ConfigMap{
			TypeMeta: meta.TypeMeta{
				Kind:       "ConfigMap",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      agentconfig.ConfigMap,
				Namespace: namespace,
			},
			Data: map[string]string{
				"echo-easy": string(rawSidecarConfig),
			},
		}, meta.CreateOptions{}).
		Return(nil, nil)

	// when
	err := s.configWatcher.Store(ctx, sidecarConfig, false)

	// then
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), map[string]map[string]string{
		namespace: {},
	}, s.configWatcher.data)
}

func (s *suiteConfigWatcher) TestStoreErrGroupBuffer() {
	// given
	ctx := k8sapi.WithK8sInterface(context.Background(), s.kubeApiMock)
	namespace := "my-app"
	s.configWatcher.data = map[string]map[string]string{namespace: {}}
	sidecarConfigA := &agentconfig.Sidecar{
		AgentName: "echo-easy-a",
		Namespace: namespace,
	}
	sidecarConfigB := &agentconfig.Sidecar{
		AgentName: "echo-easy-b",
		Namespace: namespace,
	}

	s.configMapApiMock.EXPECT().
		Get(ctx, agentconfig.ConfigMap, meta.GetOptions{}).
		DoAndReturn(func(ctx context.Context, name string, opts meta.GetOptions) (*v1.ConfigMap, error) {
			time.Sleep(50 * time.Millisecond)
			return nil, errors.NewNotFound(v1.Resource("configmap"), agentconfig.ConfigMap)
		}).Times(1)

	s.configMapApiMock.EXPECT().
		Create(gomock.Any(), gomock.Any(), meta.CreateOptions{}).
		Return(nil, nil).Times(1)

	// when
	testGroup, _ := errgroup.WithContext(ctx)
	testGroup.Go(func() error {
		return s.configWatcher.Store(ctx, sidecarConfigA, true)
	})
	testGroup.Go(func() error {
		return s.configWatcher.Store(ctx, sidecarConfigB, true)
	})
	err := testGroup.Wait()

	// then
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), 2, len(s.configWatcher.data[namespace]))
}

func TestSuiteConfigWatcher(t *testing.T) {
	suite.Run(t, new(suiteConfigWatcher))
}
