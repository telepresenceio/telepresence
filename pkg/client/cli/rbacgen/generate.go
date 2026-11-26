package rbacgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	auth "k8s.io/api/authorization/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// InstallRequest describes one run of `telepresence rbac generate install`.
type InstallRequest struct {
	ManagerNamespace string
	// Values overlays the chart defaults. ValuesSource names where they came
	// from, or is empty for the defaults alone.
	Values       *helm.Values
	ValuesSource string
	Subjects     []Subject
	Verbs        Verbs
	// All skips the sweep and emits the complete set. Client is unused then.
	All    bool
	Client kubernetes.Interface
	// Impersonated is true when --as names the checked identity.
	Impersonated bool
}

// InstallOutput is the result of GenerateInstall. Manifests is empty when
// nothing is missing.
type InstallOutput struct {
	Header    []string
	Manifests []byte
}

// WriteTo writes the header as YAML comments followed by the manifests.
func (o *InstallOutput) WriteTo(w io.Writer) (int64, error) {
	return writeCommented(w, o.Header, o.Manifests)
}

func writeCommented(w io.Writer, header []string, body []byte) (int64, error) {
	var sb strings.Builder
	for _, line := range header {
		if line == "" {
			sb.WriteString("#\n")
		} else {
			sb.WriteString("# " + line + "\n")
		}
	}
	sb.Write(body)
	n, err := io.WriteString(w, sb.String())
	return int64(n), err
}

// InstallName is the name of every generated role and binding.
func InstallName(managerNamespace string) string {
	return "telepresence-install-" + managerNamespace
}

// GenerateInstall renders the chart, finds what the checked identity lacks
// for a `telepresence helm install`, and returns RBAC that grants it.
func GenerateInstall(ctx context.Context, rq *InstallRequest) (*InstallOutput, error) {
	values := rq.Values
	if values == nil {
		values = &helm.Values{}
	}
	subjects, identity, notes, err := resolveSubjects(ctx, rq)
	if err != nil {
		return nil, err
	}

	set, err := buildInstallSet(ctx, rq.ManagerNamespace, values, rq.Verbs)
	if err != nil {
		return nil, err
	}
	var missing []*auth.ResourceAttributes
	if rq.All {
		missing = set.All()
	} else if missing, err = set.Sweep(ctx, rq.Client); err != nil {
		return nil, err
	}

	out := &InstallOutput{Header: installHeader(rq, subjects, identity, notes, len(missing) == 0)}
	if len(missing) == 0 {
		return out, nil
	}
	rs := make([]rbac.Subject, len(subjects))
	for i, s := range subjects {
		rs[i] = s.RBAC()
	}
	if out.Manifests, err = Manifests(InstallName(rq.ManagerNamespace), rs, missing); err != nil {
		return nil, err
	}
	return out, nil
}

// buildInstallSet renders the chart for values, which overlay the chart
// defaults, and returns the install set for verbs.
func buildInstallSet(ctx context.Context, managerNamespace string, values *helm.Values, verbs Verbs) (*InstallSet, error) {
	if values == nil {
		values = &helm.Values{}
	}
	render, err := RenderChart(ctx, managerNamespace, values)
	if err != nil {
		return nil, err
	}
	effective := values
	if dv, err := helm.DefaultValues(); err == nil {
		effective = helm.MergeValues(values, dv)
	}
	return HelmInstallSet(render, managerNamespace, effective, verbs)
}

// resolveSubjects returns the subjects of the bindings and the checked
// identity, or refuses when they can't be reconciled.
func resolveSubjects(ctx context.Context, rq *InstallRequest) ([]Subject, string, []string, error) {
	subjects := rq.Subjects
	if rq.All {
		if len(subjects) == 0 {
			return nil, "", nil, errcat.User.New("--all needs --subject, because there is no identity to read the subject from")
		}
		return subjects, "", nil, nil
	}
	if len(subjects) > 1 {
		return nil, "", nil, errcat.User.New("more than one --subject needs --all, since only one identity is checked")
	}
	if rq.Client == nil {
		return nil, "", nil, errors.New("no cluster client")
	}
	id, err := ReadIdentity(ctx, rq.Client)
	if err != nil {
		return nil, "", nil, err
	}

	var notes []string
	switch {
	case id == nil && len(subjects) == 0:
		return nil, "", nil, errcat.User.New(
			"unable to read the checked identity (SelfSubjectReview needs Kubernetes 1.28), so name the subject of the bindings with --subject")
	case id == nil:
		notes = append(notes, fmt.Sprintf("The cluster couldn't confirm that %s is the identity that was checked.", subjects[0]))
	case len(subjects) == 0:
		subjects = []Subject{id.Subject()}
	case !id.Matches(subjects[0]):
		return nil, "", nil, errcat.User.Newf(
			"the checked identity is %s, not %s. Run with %s to check that subject, or with --all for output that doesn't depend on an identity",
			id.Username, subjects[0], subjects[0].asFlags())
	}

	identity := "unknown"
	if id != nil {
		identity = id.Username
	}
	if subjects[0].Kind == rbac.GroupKind {
		notes = append(notes, fmt.Sprintf(
			"%s is a group. The rules reflect what one member, %s, is missing, not what every member is missing.", subjects[0], identity))
	}
	if rq.Impersonated {
		notes = append(notes,
			"The check used --as, which only knows the groups given with --as-group, so this can grant more than the subject needs.")
	}
	return subjects, identity, notes, nil
}

func installHeader(rq *InstallRequest, subjects []Subject, identity string, notes []string, nothingMissing bool) []string {
	names := make([]string, len(subjects))
	for i, s := range subjects {
		names[i] = s.String()
	}
	valuesDesc := "the chart defaults"
	if rq.ValuesSource != "" {
		valuesDesc = "the chart defaults overlaid with " + rq.ValuesSource
	}
	h := []string{
		fmt.Sprintf("RBAC for installing Telepresence %s with telepresence helm install.", version.Version),
		fmt.Sprintf("Generated by telepresence rbac generate install --verbs %s for manager namespace %s,", rq.Verbs, rq.ManagerNamespace),
		"values: " + valuesDesc + ".",
		"Subjects: " + strings.Join(names, ", ") + ".",
	}
	if rq.All {
		h = append(h, "Generated with --all, without looking at the cluster: this is the complete set, assuming nothing exists yet.")
	} else {
		h = append(h, "Checked identity: "+identity+".")
	}
	h = append(h, notes...)
	if nothingMissing {
		return append(h, "", fmt.Sprintf("%s can already install Telepresence. There is nothing to grant.", identity))
	}
	return append(h, "",
		"The chart's Roles and ClusterRoles can only be created by someone who holds what they grant,",
		"so this gives the installer everything the traffic-manager holds. In a cluster-wide install that",
		"means pod logs, evictions, workload patch, token reviews and access reviews in every namespace.",
		"Review the rules before applying them.",
		"")
}
