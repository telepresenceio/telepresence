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
// leaves ReleaseFacts zero rather than failing GatherFacts.
func (p *Prober) probeRelease(ctx context.Context) ReleaseFacts {
	if p.ReleaseLookup == nil {
		return ReleaseFacts{}
	}
	rel, err := p.ReleaseLookup(ctx, p.ManagerNamespace)
	if err != nil {
		clog.Debugf(ctx, "unable to look up existing traffic-manager release: %v", err)
		return ReleaseFacts{}
	}
	if rel == nil {
		return ReleaseFacts{}
	}
	facts := ReleaseFacts{
		Installed: true,
		Namespace: rel.Namespace,
		Values:    rel.Config,
	}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		facts.Version = strings.TrimPrefix(rel.Chart.Metadata.Version, "v")
	}
	return facts
}
