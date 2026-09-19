package setup

import (
	"context"
	"strings"

	"helm.sh/helm/v3/pkg/release"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

// NewReleaseLookup returns a Prober.ReleaseLookup that finds the
// traffic-manager Helm release through clientGetter.
func NewReleaseLookup(clientGetter genericclioptions.RESTClientGetter) func(ctx context.Context, namespace string) (*release.Release, error) {
	return func(ctx context.Context, namespace string) (*release.Release, error) {
		return helm.GetTrafficManagerRelease(ctx, clientGetter, namespace)
	}
}

// probeRelease is P6: it locates an existing traffic-manager release through
// p.ReleaseLookup. A nil ReleaseLookup disables the probe; a lookup error
// leaves ReleaseFacts zero rather than failing GatherFacts. Workload is left
// unset here; GatherFacts fills it in from the single managerWorkload fetch it
// shares with the health probe.
func (p *Prober) probeRelease(ctx context.Context) ReleaseFacts {
	empty := func() ReleaseFacts { return ReleaseFacts{Values: &helm.Values{}} }
	if p.ReleaseLookup == nil {
		return empty()
	}
	rel, err := p.ReleaseLookup(ctx, p.ManagerNamespace)
	if err != nil {
		clog.Debugf(ctx, "unable to look up existing traffic-manager release: %v", err)
		return empty()
	}
	if rel == nil {
		return empty()
	}
	facts := ReleaseFacts{
		Installed: true,
		Namespace: rel.Namespace,
		Values:    &helm.Values{},
	}
	if rel.Config != nil {
		if vals, err := helm.ValuesFromMap(rel.Config); err != nil {
			facts.ValuesError = err.Error()
		} else {
			facts.Values = vals
		}
	}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		facts.Version = strings.TrimPrefix(rel.Chart.Metadata.Version, "v")
	}
	return facts
}
