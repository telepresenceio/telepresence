package install

import (
	"context"
	"strings"
	"testing"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// managerReleaseName is the traffic-manager helm release/Deployment name
// every telepresence-oss chart install uses (rt's own helmReleaseName,
// unexported there).
const managerReleaseName = "traffic-manager"

// trafficManagerDeployment is the kubectl resource path for the
// traffic-manager Deployment.
const trafficManagerDeployment = "deploy/" + managerReleaseName

// pullPolicyFor mirrors rt's own unexported helper (framework/rt/env.go): the
// image.pullPolicy a raw `telepresence helm install|upgrade` needs so a
// locally built (or intentionally bogus) image resolves from the node's own
// image cache instead of attempting a real pull.
func pullPolicyFor(registry string) string {
	switch {
	case registry == "local":
		return "Never"
	case strings.HasPrefix(registry, "localhost:"):
		return "Always"
	default:
		return ""
	}
}

// helmSetArgs returns the --set flags identifying the manager image
// (registry, tag, pull policy) plus a namespaces={ns} scope: the minimum a
// bare `telepresence helm install|upgrade` needs to bring up a working
// traffic-manager confined to ns. tag overrides the version-under-test's own
// tag when non-empty.
func helmSetArgs(r *rt.Runtime, ns, tag string) []string {
	if tag == "" {
		tag = r.Version().String()
	}
	args := []string{
		"--set", "namespaces={" + ns + "}",
		"--set", "image.registry=" + r.Registry(),
		"--set", "image.tag=" + tag,
	}
	if pp := pullPolicyFor(r.Registry()); pp != "" {
		args = append(args, "--set", "image.pullPolicy="+pp)
	}
	return args
}

// helmInstallArgs is the full `telepresence helm install` argument list for a
// working release confined to ns, using the version under test's own image.
func helmInstallArgs(r *rt.Runtime, ns string) []string {
	return append([]string{"helm", "install", "--manager-namespace", ns}, helmSetArgs(r, ns, "")...)
}

// requireManagerReady waits for the traffic-manager Deployment in ns to
// finish rolling out, failing t if it doesn't.
func requireManagerReady(t testing.TB, ctx context.Context, r *rt.Runtime, ns string) {
	t.Helper()
	if _, err := r.Kubectl(ctx, ns, "rollout", "status", trafficManagerDeployment, "--timeout=120s"); err != nil {
		t.Fatalf("waiting for traffic-manager in %s: %v", ns, err)
	}
}

// requireManagerAbsent fails t if the traffic-manager Deployment still
// exists in ns.
func requireManagerAbsent(t testing.TB, ctx context.Context, r *rt.Runtime, ns string) {
	t.Helper()
	if _, err := r.Kubectl(ctx, ns, "get", trafficManagerDeployment); err == nil {
		t.Fatalf("traffic-manager still present in %s after uninstall", ns)
	}
}

// managerLogLevel returns the traffic-manager Deployment's LOG_LEVEL env var
// in ns: the chart's rendering of the logLevel value
// (charts/telepresence-oss/templates/deployment.yaml).
func managerLogLevel(t testing.TB, ctx context.Context, r *rt.Runtime, ns string) string {
	t.Helper()
	var dep struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := r.KubectlJSON(ctx, ns, &dep, "get", trafficManagerDeployment); err != nil {
		t.Fatalf("reading traffic-manager deployment in %s: %v", ns, err)
	}
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "LOG_LEVEL" {
				return e.Value
			}
		}
	}
	return ""
}

// releaseExists reports whether a traffic-manager helm release secret exists
// in ns: the same check rt's own manager fixtures use internally
// (fixture_manager.go's managerReleaseExists, unexported there).
func releaseExists(ctx context.Context, r *rt.Runtime, ns string) bool {
	out, err := r.Kubectl(ctx, ns, "get", "secret", "-l", "owner=helm", "-o", "name")
	if err != nil {
		return false
	}
	return strings.Contains(out, "sh.helm.release.v1."+managerReleaseName+".")
}
