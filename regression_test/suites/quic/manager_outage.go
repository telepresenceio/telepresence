package quic

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// trafficManagerDeployment is the chart-created Deployment name for the
// shared traffic-manager release (rt/fixture_manager.go's unexported
// helmReleaseName), mirroring helpers.go's quicForwarderDeployment.
const trafficManagerDeployment = "traffic-manager"

// quicManagerOutageTermTimeout bounds the wait for the traffic-manager pod
// to actually terminate after scaling to zero.
const quicManagerOutageTermTimeout = 60 * time.Second

// quicOutageProbeTimeout/quicOutageProbeInterval bound the in-cluster curl
// poll against the intercepted workload while the manager is down.
const (
	quicOutageProbeTimeout  = 30 * time.Second
	quicOutageProbeInterval = 3 * time.Second
)

// quicManagerOutageDetachTimeout/-Interval bound detachRetrying's retry
// loop.
const (
	quicManagerOutageDetachTimeout  = 30 * time.Second
	quicManagerOutageDetachInterval = 2 * time.Second
)

// ManagerOutage is the decisive test for the forwarder architecture
// described in "The forwarder" section of
// docs/reference/quic-transport-architecture.md: "The manager dying no
// longer affects client<->agent traffic at all: the forwarder routes
// packets and the agents terminate their own TLS, so attachments keep
// flowing through a manager restart exactly as they do today." With a
// sidecar intercept active and its agent connection already confirmed on
// quic, it scales the traffic-manager Deployment to zero, confirms the
// intercepted round trip keeps working while the manager is entirely gone,
// then scales the manager back up and confirms both the manager-bound
// tunnel and the agent attachment recover.
//
// The client session's own connection to the manager is expected to error
// out and reconnect around this outage on its own (session keepalives,
// watches, etc.); that is not what this test is about and is not asserted
// one way or the other -- in particular, recovery is polled with
// awaitStatusTransportPrefix/awaitAgentTransport (no quit/reconnect issued
// by the test itself), the framework's sanctioned way to observe a
// same-session recovery without disturbing the live intercept a reconnect
// would drop. What must hold throughout is the client<->agent data path for
// cluster-originated traffic: an in-cluster request to the intercepted
// workload is tunneled agent -> forwarder -> laptop handler, which never
// touches the manager. (Requests the developer makes from the laptop
// through the VPN are a different path: they need the manager tunnel for
// cluster DNS and subnet routing, so those are not expected to survive and
// are not asserted here.)
//
// This suite connects to its own fresh PrivateNamespace, like Outage, and
// runs the manager scale-down through rt.Mutate(rt.ManagerFixture(...)):
// unlike the quic-forwarder (a plain Deployment with no fixture of its
// own), the traffic-manager IS a memoized fixture other suites' SetupTest
// relies on being live and correctly configured. Mutate's end-of-test
// invalidation is what makes that safe -- the next suite to touch the
// shared manager fixture re-provisions (a helm upgrade, which waits for the
// rollout and for the old pod to be fully gone) rather than trusting this
// suite's own manual scale-back-up to have left it in an equivalent state.
// Labeled Slow: it waits through a full manager-pod termination, an
// in-cluster outage probe, and the client's own multi-step recovery
// (control-plane reconnect, then transport re-probe).
type ManagerOutage struct {
	rt.Suite
}

func init() {
	rt.Register(&ManagerOutage{}, rt.InArea("quic"), rt.NeedsManager(managers.QuicNodePort()), rt.WithLabels(rt.Slow))
}

func (s *ManagerOutage) Test_AttachmentSurvivesManagerOutage() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	tp := s.CLI()

	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateNamespace(env, "quic-manager-outage")
	// ns is brand new: the already-running manager only manages it once its
	// pod restarts and re-lists namespaces.
	s.Require().NoError(rt.RestartManager(env))
	wl := rt.Get(t, rt.WorkloadFixture(ns, workloads.Echo("quic-manager-outage")))
	ls := s.LocalEcho()

	// Free whatever the default connection currently holds before
	// connecting fresh to ns: the manager restart above would invalidate a
	// connection made before it.
	rt.Mutate(t, rt.ConnectionFixture(s.AppNamespace())).Disconnect(t)
	conn := awaitTransportPrefix(t, ctx, tp, ns, rt.Reconnect(t, ctx, ns))

	// Detached via detachRetrying below/deferred, not Attach.Detach: see its
	// doc for why a plain one-shot detach right after this test's outage is
	// brittle. The captured *Attach carries nothing this test needs, so its
	// return value is discarded.
	conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	mustDetach := true
	defer func() {
		if mustDetach {
			detachRetrying(t, ctx, tp, wl.Name, ns)
		}
	}()

	// Baseline: intercepted traffic works and the agent attachment is
	// already on quic before the outage starts.
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	awaitAgentTransport(t, ctx, tp, wl.Name)

	// Manager churn: declare it through Mutate before touching the
	// Deployment directly, so a later suite's Get re-provisions instead of
	// trusting this test's own manual restore.
	rt.Mutate(t, rt.ManagerFixture(managers.QuicNodePort()))

	scaleManagerDown(t, ctx, r)
	restoreManager := true
	defer func() {
		if restoreManager {
			scaleManagerUp(t, ctx, r)
		}
	}()

	// The decisive assertion: a request that originates in the cluster and
	// hits the intercepted workload still reaches the client's local
	// handler with no traffic-manager pod at all. Deliberately not
	// RoutedToLocal (as the baseline above uses): that pings the service
	// through the client's VPN, whose DNS resolution and subnet routing run
	// over the manager-bound tunnel and so legitimately cannot work while
	// the manager is gone. Driven from an in-cluster pod instead
	// (curlimages/curl).
	awaitInClusterRouteToLocal(t, ctx, r, ns, wl.ServiceURL(), ls)

	scaleManagerUp(t, ctx, r)
	restoreManager = false

	// Recovery, same session: no quit/reconnect (see the type doc). Bound
	// comfortably above the client's own re-probe interval to also absorb
	// the forwarder's relearning delay.
	awaitStatusTransportPrefix(t, ctx, tp, quicPrefix, quicRecoveryTimeout)

	// The client<->agent attachment's own QUIC connection is independent of
	// the manager (see the decisive assertion above) and is not expected to
	// have tripped at all during the outage; confirm it is still serving
	// quic once the manager-bound tunnel has also recovered.
	awaitAgentTransport(t, ctx, tp, wl.Name)

	detachRetrying(t, ctx, tp, wl.Name, ns)
	mustDetach = false
}

// scaleManagerDown scales the shared traffic-manager Deployment to zero
// replicas and waits for its pod to actually terminate: `rollout status`
// alone would report success as soon as the scale is accepted, before the
// pod is actually gone. Mirrors helpers.go's scaleQuicForwarderDown, for
// deploy/traffic-manager rather than deploy/quic-forwarder.
func scaleManagerDown(t testing.TB, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	if _, err := r.Kubectl(ctx, mgrNS, "scale", "deploy/"+trafficManagerDeployment, "--replicas", "0"); err != nil {
		t.Fatalf("scale %s to 0: %v", trafficManagerDeployment, err)
	}
	deadline := time.Now().Add(quicManagerOutageTermTimeout)
	for {
		out, err := r.Kubectl(ctx, mgrNS, "get", "pods", "-l", "app=traffic-manager", "-o", "name")
		if err == nil && strings.TrimSpace(out) == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("traffic-manager pod did not terminate after scaling to 0")
		}
		time.Sleep(quicPollInterval)
	}
}

// scaleManagerUp scales the shared traffic-manager Deployment back to one
// replica and waits for the rollout to report ready.
func scaleManagerUp(t testing.TB, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	if _, err := r.Kubectl(ctx, mgrNS, "scale", "deploy/"+trafficManagerDeployment, "--replicas", "1"); err != nil {
		t.Fatalf("scale %s to 1: %v", trafficManagerDeployment, err)
	}
	if _, err := r.Kubectl(ctx, mgrNS, "rollout", "status", "deploy/"+trafficManagerDeployment, "--timeout=120s"); err != nil {
		t.Fatalf("rollout status %s: %v", trafficManagerDeployment, err)
	}
}

// inClusterProbeImage is the ephemeral pod image the in-cluster outage
// probe curls from.
const inClusterProbeImage = "curlimages/curl"

// awaitInClusterRouteToLocal polls an in-cluster curl (`kubectl run --rm
// -i`, run fresh each attempt since --rm pods can't be reused) against url,
// launched in ns, until the response body carries ls's marker, or fails t
// after quicOutageProbeTimeout.
func awaitInClusterRouteToLocal(t testing.TB, ctx context.Context, r *rt.Runtime, ns, url string, ls *rt.LocalService) {
	t.Helper()
	deadline := time.Now().Add(quicOutageProbeTimeout)
	for probe := 0; ; probe++ {
		podName := "quic-manager-outage-probe-" + strconv.Itoa(probe)
		out, err := r.Kubectl(ctx, ns, "run", "-i", podName, "--rm", "--image", inClusterProbeImage,
			"--restart", "Never", "--command", "--", "curl", "--silent", "--max-time", "5", url)
		if err == nil && strings.Contains(out, ls.Marker()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("in-cluster request to %s never reached the local handler while the manager was down", url)
		}
		time.Sleep(quicOutageProbeInterval)
	}
}

// detachRetrying detaches from workload in ns, retrying briefly on failure.
// Right after the traffic-manager pod is replaced, the userd connector's
// own control-plane connection to it can lag a few seconds behind both this
// suite's own recovery polls (which only wait on tunnel_transport/
// agent_transports) and the manager pod actually being reachable for
// detach's own pod-resolution lookup (pkg/client/portforward/resolve.go's
// ResolveSvcToPod, done fresh on every dial rather than cached). A plain
// one-shot detach attempted in that window fails with "no running pods with
// accessible ports found for service"; retrying absorbs it without
// weakening what the test actually asserts.
func detachRetrying(t testing.TB, ctx context.Context, tp *cli.TP, workload, ns string) {
	t.Helper()
	deadline := time.Now().Add(quicManagerOutageDetachTimeout)
	for {
		if _, _, err := tp.Run(ctx, "detach", workload, "-n", ns); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("detach %q kept failing", workload)
		}
		time.Sleep(quicManagerOutageDetachInterval)
	}
}
