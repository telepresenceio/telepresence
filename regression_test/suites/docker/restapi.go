package docker

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// restAPIPort is the telepresenceAPI.port restAPISpec configures: the
// traffic-agent listens on it inside the pod's network namespace, reachable
// from any container in the pod via localhost. It starts listening
// unconditionally once the agent's config carries a non-zero API port
// (cmd/traffic/cmd/agent/agent.go's `if ac.APIPort != 0 { g.Go("API-server",
// ...) }`), independent of whether an intercept is active.
const restAPIPort = 9980

// restAPIHeaderKey/restAPIHeaderVal filter the intercepts this suite's tests
// create (Test_ConsumeHere, Test_InterceptInfo). A probe request carrying
// this header is one that would match the intercept; a probe without it
// would not. The agent-side consume-here/intercept-info decision
// (cmd/traffic/cmd/agent/fwdstate.go's InterceptInfo) only skips an
// intercept when the caller sends a non-empty
// restapi.HeaderCallerInterceptID that doesn't match that intercept's ID;
// since this suite's probes never send that header and only ever have one
// active intercept to consider, the header filter alone is enough to drive
// the decision. Every probe below queries the agent's own API server;
// the client-side API server a local `telepresence intercept` handler also
// runs is a different axis, out of scope for this suite.
const (
	restAPIHeaderKey = "x-rtest-restapi"
	restAPIHeaderVal = "match"
)

// restAPIMetadataKey/restAPIMetadataVal are the --metadata key/value pair
// Test_InterceptInfo attaches to its intercept: the /intercept-info probe
// asserts they come back in the response's metadata map.
const (
	restAPIMetadataKey = "my"
	restAPIMetadataVal = "data"
)

// apiPollTimeout/apiPollInterval bound every consume-here probe poll below:
// the agent's REST API server starts in its own goroutine
// (cmd/traffic/cmd/agent/agent.go), which can still be racing the pod's own
// readiness by the time this suite's rollout wait returns, and an
// intercept's ACTIVE disposition (which the CLI already waits for) can
// similarly lag the manager's review reaching this specific agent pod.
const (
	apiPollTimeout  = 30 * time.Second
	apiPollInterval = time.Second
)

// restAPISpec is a manager spec inline to this suite (its Key/Values shape
// is managers.Spec from framework/managers/spec.go): the chart's
// telepresenceAPI.port, restricted to the field managers.TelepresenceAPI
// (framework/managers/values.go) exposes. Built inline rather than added to
// the catalog since no other suite needs it.
//
//nolint:gochecknoglobals // registered once at init time, like every other manager spec
var restAPISpec = managers.Spec{
	Key:    "api-port",
	Values: managers.Values{TelepresenceAPI: managers.TelepresenceAPI{Port: restAPIPort}},
}

// RestAPI proves the traffic-agent sidecar's embedded REST API server
// (telepresenceAPI.port) answers /consume-here and /intercept-info from
// inside the cluster, queried with `kubectl exec ... wget` from the
// workload's own app container: it shares the pod's network namespace with
// the traffic-agent, so localhost:<port> reaches the sidecar directly. This
// is simpler than restapi_test.go's /forward-based round trip, which needed
// the target's own TELEPRESENCE_API_HOST/PORT env plumbing that this
// framework's plain workloads.Echo template doesn't carry, and exercises the
// same agent-side consume-here decision restapi_test.go's
// Test_RestAPI_FilteredConsume "query-remote-*" cases did
// (integration_test/restapi_test.go). The workload carries
// annotation.InjectTrafficAgent so the agent (and its API server) is present
// from the pod's first rollout: the default OnDemand injectPolicy would
// otherwise leave the pod agent-less, and every probe below would find
// nothing listening on restAPIPort, until something actually requests an
// intercept.
type RestAPI struct {
	rt.Suite
}

func init() {
	rt.Register(&RestAPI{},
		rt.InArea("docker"),
		rt.NeedsManager(restAPISpec),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// firstPodName returns the name of the first pod matching label app=app in
// ns; the workload fixture's rollout wait guarantees at least one exists and
// is ready by the time a suite calls this.
func firstPodName(t testing.TB, ctx context.Context, r *rt.Runtime, ns, app string) string {
	t.Helper()
	out, err := r.Kubectl(ctx, ns, "get", "pods", "-l", "app="+app, "-o", "jsonpath={.items[0].metadata.name}")
	name := strings.TrimSpace(out)
	if err != nil || name == "" {
		t.Fatalf("no pod found for app=%s in %s: %v", app, ns, err)
	}
	return name
}

// wgetConsumeHereArgs builds a `wget` command probing the sidecar's
// consume-here endpoint on restAPIPort, carrying headers (nil for none) and
// scoped to containerPort via the endpoint's own query parameter (api.go's
// containerPort FormValue).
func wgetConsumeHereArgs(headers map[string]string, containerPort int) []string {
	args := []string{"wget", "-q", "-O", "-"}
	for k, v := range headers {
		args = append(args, "--header", k+": "+v)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s?containerPort=%d", restAPIPort, restapi.EndPointConsumeHere, containerPort)
	return append(args, url)
}

// queryConsumeHere runs the wget probe against podName's own app container
// and decodes the JSON boolean the sidecar's consume-here endpoint returns.
func queryConsumeHere(
	ctx context.Context, r *rt.Runtime, podName string, wl *rt.Workload, headers map[string]string,
) (bool, error) {
	args := append([]string{"exec", podName, "-c", wl.Name, "--"}, wgetConsumeHereArgs(headers, wl.Port)...)
	out, err := r.Kubectl(ctx, wl.Namespace, args...)
	if err != nil {
		return false, fmt.Errorf("kubectl exec wget consume-here: %w: %s", err, out)
	}
	var consume bool
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &consume); err != nil {
		return false, fmt.Errorf("consume-here body %q: %w", out, err)
	}
	return consume, nil
}

// wgetInterceptInfoArgs builds a `wget` command probing the sidecar's
// intercept-info endpoint on restAPIPort, carrying headers (nil for none)
// and scoped to containerPort via the endpoint's own query parameter (api.go's
// containerPort FormValue).
func wgetInterceptInfoArgs(headers map[string]string, containerPort int) []string {
	args := []string{"wget", "-q", "-O", "-"}
	for k, v := range headers {
		args = append(args, "--header", k+": "+v)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s?containerPort=%d", restAPIPort, restapi.EndPointInterceptInfo, containerPort)
	return append(args, url)
}

// queryInterceptInfo runs the wget probe against podName's own app container
// and decodes the JSON object the sidecar's intercept-info endpoint returns
// (pkg/restapi.InterceptInfo).
func queryInterceptInfo(
	ctx context.Context, r *rt.Runtime, podName string, wl *rt.Workload, headers map[string]string,
) (*restapi.InterceptInfo, error) {
	args := append([]string{"exec", podName, "-c", wl.Name, "--"}, wgetInterceptInfoArgs(headers, wl.Port)...)
	out, err := r.Kubectl(ctx, wl.Namespace, args...)
	if err != nil {
		return nil, fmt.Errorf("kubectl exec wget intercept-info: %w: %s", err, out)
	}
	var info restapi.InterceptInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &info); err != nil {
		return nil, fmt.Errorf("intercept-info body %q: %w", out, err)
	}
	return &info, nil
}

// Test_ConsumeHere connects, creates a header-filtered intercept, and polls
// the sidecar's consume-here decision from inside the pod: a probe request
// that doesn't carry the filter header should be consumed by the agent
// (Intercepted=false, since it doesn't match), while one that does should
// not (Intercepted=true, so the agent defers to the client).
func (s *RestAPI) Test_ConsumeHere() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	conn := s.Connect()

	tpl := workloads.Echo("restapi")
	tpl.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	wl := s.Workload(tpl)

	podName := firstPodName(t, ctx, r, wl.Namespace, wl.Name)

	// probe polls queryConsumeHere until it settles on want, capturing the
	// last result/error for the failure message so a still-broken next run
	// is diagnosable.
	probe := func(headers map[string]string, want bool, msg string) {
		t.Helper()
		var last bool
		var lastErr error
		s.Eventually(func() bool {
			last, lastErr = queryConsumeHere(ctx, r, podName, wl, headers)
			return lastErr == nil && last == want
		}, apiPollTimeout, apiPollInterval, "%s (last result %v, last error %v)", msg, last, lastErr)
	}

	probe(nil, true, "an unintercepted probe should be consumed by the agent")

	ls := s.LocalEcho()
	filter := cli.HTTPHeader(restAPIHeaderKey, restAPIHeaderVal)
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), filter)
	defer a.Detach(t)
	s.Eventually(func() bool { return attached(conn.List(t), wl.Name, wl.Namespace) },
		attachTimeout, attachPollInterval, "intercept did not appear in list")

	probe(nil, true, "a non-matching probe should still be consumed by the agent")
	probe(map[string]string{restAPIHeaderKey: restAPIHeaderVal}, false,
		"a probe matching the intercept's filter should not be consumed by the agent")
}

// Test_InterceptInfo connects, probes /intercept-info with no intercept
// active, then creates a header-filtered intercept carrying a --metadata
// key/value pair and polls the sidecar's intercept-info decision from inside
// the pod: a probe that doesn't carry the filter header should see
// Intercepted=false, while one that does should see Intercepted=true and
// find the CLI-supplied pair in the response's metadata map.
func (s *RestAPI) Test_InterceptInfo() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	conn := s.Connect()

	tpl := workloads.Echo("restapi")
	tpl.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	wl := s.Workload(tpl)

	podName := firstPodName(t, ctx, r, wl.Namespace, wl.Name)

	// probe polls queryInterceptInfo until its Intercepted field settles on
	// want, capturing the last result/error for the failure message so a
	// still-broken next run is diagnosable.
	probe := func(headers map[string]string, want bool, msg string) *restapi.InterceptInfo {
		t.Helper()
		var last *restapi.InterceptInfo
		var lastErr error
		s.Eventually(func() bool {
			last, lastErr = queryInterceptInfo(ctx, r, podName, wl, headers)
			return lastErr == nil && last != nil && last.Intercepted == want
		}, apiPollTimeout, apiPollInterval, "%s (last result %+v, last error %v)", msg, last, lastErr)
		return last
	}

	probe(nil, false, "a probe with no intercept active should report Intercepted=false")

	ls := s.LocalEcho()
	filter := cli.HTTPHeader(restAPIHeaderKey, restAPIHeaderVal)
	meta := cli.Metadata(restAPIMetadataKey, restAPIMetadataVal)
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), filter, meta)
	defer a.Detach(t)
	s.Eventually(func() bool { return attached(conn.List(t), wl.Name, wl.Namespace) },
		attachTimeout, attachPollInterval, "intercept did not appear in list")

	info := probe(map[string]string{restAPIHeaderKey: restAPIHeaderVal}, true,
		"a probe matching the intercept's filter should report Intercepted=true")
	if got := info.Metadata[restAPIMetadataKey]; got != restAPIMetadataVal {
		t.Fatalf("intercept-info metadata[%q] = %q, want %q", restAPIMetadataKey, got, restAPIMetadataVal)
	}

	probe(nil, false, "a non-matching probe should report Intercepted=false")
}
