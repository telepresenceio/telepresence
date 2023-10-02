package trafficmgr

import (
	"context"

	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func (s *session) getCurrentSidecarsInNamespace(ctx context.Context, ns string) map[string]*agentconfig.Sidecar {
	// Load configmap entry from the telepresence-agents configmap
	cm, err := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(ns).Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
	if err != nil {
		if !k8sErrors.IsNotFound(err) {
			dlog.Error(ctx, errcat.User.New(err))
		}
		return nil
	}

	if cm.Data == nil {
		dlog.Debugf(ctx, "unable to read data in configmap %q", agentconfig.ConfigMap)
	}

	sidecars := make(map[string]*agentconfig.Sidecar, len(cm.Data))
	for workload, sidecar := range cm.Data {
		var cfg agentconfig.Sidecar
		if err = yaml.Unmarshal([]byte(sidecar), &cfg); err != nil {
			dlog.Errorf(ctx, "Unable to parse entry for %q in configmap %q: %v", workload, agentconfig.ConfigMap, err)
			continue
		}
		sidecars[workload] = &cfg
	}

	return sidecars
}
