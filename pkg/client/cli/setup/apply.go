package setup

import (
	"context"
	"encoding/json/v2"
	"io"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

// Apply installs or upgrades the traffic-manager with the proposal's values.
// It hands the marshaled values to helm.EnsureTrafficManager directly (not
// through helm.Request.Run, which would recompute ValuesJson from its
// flag-based options and discard them), so it inherits the Atomic/Wait
// semantics and the event-watch abort diagnostics. ctx must carry the client
// configuration (timeouts). An ActionNone proposal is a no-op.
func Apply(ctx context.Context, clientGetter genericclioptions.RESTClientGetter, managerNamespace string, p *Proposal, out io.Writer) error {
	var rt helm.RequestType
	switch p.Action {
	case ActionInstall:
		rt = helm.Install
	case ActionUpgrade:
		rt = helm.Upgrade
	default:
		return nil
	}
	valuesJSON, err := json.Marshal(p.Values)
	if err != nil {
		return err
	}
	req := &helm.Request{
		Type:            rt,
		ValuesJson:      valuesJSON,
		CreateNamespace: true,
		Version:         p.Version,
	}
	if err := helm.EnsureTrafficManager(ctx, clientGetter, managerNamespace, req); err != nil {
		return err
	}
	ioutil.Printf(out, "Traffic Manager %s successfully\n", ApplyOutcome(p.Action))
	return nil
}

// ApplyOutcome names what an apply of the given action did.
func ApplyOutcome(a Action) string {
	if a == ActionUpgrade {
		return "upgraded"
	}
	return "installed"
}
