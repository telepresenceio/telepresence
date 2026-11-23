package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
)

// kafkaGroupVersion is the API group/version the Kafka provider registers
// its KafkaSplit and KafkaRoute CRDs under.
const kafkaGroupVersion = "kafka.telepresence.io/v1alpha1"

// kafkaSplitsResource and kafkaRoutesResource are the plural resource names
// of the Kafka provider's two CRDs.
const (
	kafkaSplitsResource = "splits"
	kafkaRoutesResource = "routes"
)

// argoGroupVersion and argoRolloutsResource are the API group/version and
// resource name Argo Rollouts registers, checked as an informational signal
// for the Kafka provider's Argo Rollouts integration.
const (
	argoGroupVersion     = "argoproj.io/v1alpha1"
	argoRolloutsResource = "rollouts"
)

// kafkaSplitsPath is the cluster-wide REST path listing every KafkaSplit.
const kafkaSplitsPath = "/apis/" + kafkaGroupVersion + "/" + kafkaSplitsResource

// KafkaFacts are the read-only prerequisites for the Kafka personal-intercept
// provider: whether its CRDs and Argo Rollouts are served, and how many
// KafkaSplit resources are currently active.
type KafkaFacts struct {
	CRDs             Finding `json:"crds"`         // splits and routes served under kafka.telepresence.io/v1alpha1
	ArgoRollouts     Finding `json:"argoRollouts"` // argoproj.io/v1alpha1 rollouts served; informational
	ActiveSplits     int     `json:"activeSplits"` // KafkaSplit resources whose status.phase is not Disabled
	SplitsListDenied bool    `json:"splitsListDenied,omitempty"`
	SplitsListError  string  `json:"splitsListError,omitempty"`
}

// probeKafka records the Kafka provider's cluster prerequisites: whether its
// CRDs and Argo Rollouts are served, and, when the CRDs are served, how many
// KafkaSplit resources are currently active.
func (p *Prober) probeKafka(ctx context.Context) KafkaFacts {
	facts := KafkaFacts{
		CRDs:         kafkaCRDsFinding(ctx, p.KubeClient),
		ArgoRollouts: argoRolloutsFinding(ctx, p.KubeClient),
	}
	if facts.CRDs.Verdict != VerdictYes {
		return facts
	}

	splits, err := p.kafkaSplits(ctx)
	switch {
	case err == nil:
		for i := range splits {
			phase, _, _ := unstructured.NestedString(splits[i].Object, "status", "phase")
			if phase != string(api.SplitPhaseDisabled) {
				facts.ActiveSplits++
			}
		}
	case apierrors.IsForbidden(err):
		facts.SplitsListDenied = true
	default:
		facts.SplitsListError = err.Error()
	}
	return facts
}

// kafkaSplits lists every KafkaSplit, using p.KafkaSplits when set, else
// defaultKafkaSplits.
func (p *Prober) kafkaSplits(ctx context.Context) ([]unstructured.Unstructured, error) {
	if p.KafkaSplits != nil {
		return p.KafkaSplits(ctx)
	}
	return defaultKafkaSplits(ctx, p.KubeClient)
}

// defaultKafkaSplits lists every KafkaSplit through the discovery client's
// REST client, since no typed or dynamic client exists for the Kafka
// provider's CRDs. A fake clientset's discovery REST client is nil, which is
// why Prober.KafkaSplits exists as an injectable override for tests.
func defaultKafkaSplits(ctx context.Context, ki kubernetes.Interface) ([]unstructured.Unstructured, error) {
	rc := ki.Discovery().RESTClient()
	if rc == nil {
		return nil, errors.New("no REST client")
	}
	raw, err := rc.Get().AbsPath(kafkaSplitsPath).Do(ctx).Raw()
	if err != nil {
		return nil, err
	}
	var list unstructured.UnstructuredList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode KafkaSplits: %w", err)
	}
	return list.Items, nil
}

// kafkaCRDsFinding reports whether the Kafka provider's splits and routes
// resources are both served.
func kafkaCRDsFinding(ctx context.Context, ki kubernetes.Interface) Finding {
	return groupResourcesFinding(ctx, ki, kafkaGroupVersion, kafkaSplitsResource, kafkaRoutesResource)
}

// argoRolloutsFinding reports whether Argo Rollouts' rollouts resource is
// served; it is informational and never gates the Kafka provider.
func argoRolloutsFinding(ctx context.Context, ki kubernetes.Interface) Finding {
	return groupResourcesFinding(ctx, ki, argoGroupVersion, argoRolloutsResource)
}

// groupResourcesFinding reports whether groupVersion is served and every
// named resource appears in it, the technique certManagerFinding uses for a
// single resource.
func groupResourcesFinding(ctx context.Context, ki kubernetes.Interface, groupVersion string, resources ...string) Finding {
	list, err := ki.Discovery().ServerResourcesForGroupVersionWithContext(ctx, groupVersion)
	switch {
	case apierrors.IsNotFound(err):
		return Finding{Verdict: VerdictNo, Evidence: []string{"the " + groupVersion + " API group is not served"}}
	case err != nil:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf("%s discovery failed: %v", groupVersion, err)}}
	}
	served := make(map[string]bool, len(list.APIResources))
	for _, r := range list.APIResources {
		served[r.Name] = true
	}
	var missing []string
	for _, res := range resources {
		if !served[res] {
			missing = append(missing, res)
		}
	}
	if len(missing) > 0 {
		return Finding{Verdict: VerdictNo, Evidence: []string{
			groupVersion + " is served but its " + strings.Join(missing, ", ") + " resource was not found",
		}}
	}
	return Finding{Verdict: VerdictYes, Evidence: []string{
		"the " + groupVersion + " " + strings.Join(resources, ", ") + " resource is served",
	}}
}
