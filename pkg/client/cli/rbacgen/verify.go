package rbacgen

import (
	"context"
	"fmt"
	"io"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

// VerifyInstallRequest describes one run of `telepresence rbac verify install`.
type VerifyInstallRequest struct {
	ManagerNamespace string
	// Values overlays the chart defaults. ValuesSource names where they came
	// from, or is empty for the defaults alone.
	Values       *helm.Values
	ValuesSource string
	Verbs        Verbs
	Client       kubernetes.Interface
}

// InstallVerdict is the result of VerifyInstall.
type InstallVerdict struct {
	ManagerNamespace string            `json:"managerNamespace"`
	Verbs            Verbs             `json:"verbs"`
	Values           string            `json:"values"`
	Identity         string            `json:"identity,omitempty"`
	Missing          []DeniedAttribute `json:"missing"`
}

// OK reports whether nothing is missing.
func (v *InstallVerdict) OK() bool {
	return len(v.Missing) == 0
}

// VerifyInstall runs the sweep of `rbac generate install` and returns what
// the checked identity lacks.
func VerifyInstall(ctx context.Context, rq *VerifyInstallRequest) (*InstallVerdict, error) {
	set, err := buildInstallSet(ctx, rq.ManagerNamespace, rq.Values, rq.Verbs)
	if err != nil {
		return nil, err
	}
	denied, err := set.Sweep(ctx, rq.Client)
	if err != nil {
		return nil, err
	}
	v := &InstallVerdict{
		ManagerNamespace: rq.ManagerNamespace,
		Verbs:            rq.Verbs,
		Values:           valuesDescription(rq.ValuesSource),
		Missing:          make([]DeniedAttribute, len(denied)),
	}
	for i, ra := range denied {
		v.Missing[i] = ToDeniedAttribute(ra)
	}
	if id, err := ReadIdentity(ctx, rq.Client); err == nil && id != nil {
		v.Identity = id.Username
	}
	return v, nil
}

func valuesDescription(source string) string {
	if source == "" {
		return "the chart defaults"
	}
	return "the chart defaults overlaid with " + source
}

// WriteText writes the verdict for a human reader.
func (v *InstallVerdict) WriteText(w io.Writer) {
	who := v.Identity
	if who == "" {
		who = "The checked identity"
	}
	ioutil.Printf(w, "Checked telepresence helm install --verbs %s in manager namespace %s, with %s.\n", v.Verbs, v.ManagerNamespace, v.Values)
	if v.OK() {
		ioutil.Printf(w, "%s can install Telepresence. Nothing is missing.\n", who)
		return
	}
	ioutil.Printf(w, "%s is missing %d permissions:\n", who, len(v.Missing))
	for _, d := range v.Missing {
		ioutil.Printf(w, "  %s\n", d)
	}
	ioutil.Println(w, "Run telepresence rbac generate install with the same flags for RBAC that grants them.")
}

// String renders the attribute like "get configmaps in namespace ambassador
// (traffic-manager)".
func (d DeniedAttribute) String() string {
	var sb strings.Builder
	sb.WriteString(d.Verb + " " + d.Resource)
	if d.Subresource != "" {
		sb.WriteString("/" + d.Subresource)
	}
	if d.Group != "" {
		sb.WriteString("." + d.Group)
	}
	if d.Namespace != "" {
		sb.WriteString(" in namespace " + d.Namespace)
	}
	if d.Name != "" {
		fmt.Fprintf(&sb, " (%s)", d.Name)
	}
	return sb.String()
}
