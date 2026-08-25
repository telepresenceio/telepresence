package k8s

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/cenkalti/backoff/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	empty "google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const (
	// trafficManagerServiceName is the ClusterIP Service in front of the
	// traffic-manager pod, used by the discovery fallback.
	trafficManagerServiceName = "traffic-manager"

	// trafficManagerPodName is the deterministic StatefulSet pod name a
	// phase-3 chart installs. Older charts run a Deployment with a random
	// pod name, so the known-name dial fails and discovery takes over.
	trafficManagerPodName = "traffic-manager-0"

	trafficManagerAPIPort = 8081

	// knownNameProbeTimeout bounds the first known-name attempt so an old
	// Deployment install falls back to discovery quickly instead of
	// spending the whole connect budget on a dial that can't succeed.
	knownNameProbeTimeout = 5 * time.Second
)

// connectResult is the outcome of a manager connect attempt: a connection
// plus the manager's name and version, or an error.
type connectResult struct {
	conn *grpc.ClientConn
	name string
	ver  semver.Version
	err  error
}

func (r connectResult) get() (*grpc.ClientConn, string, semver.Version, error) {
	return r.conn, r.name, r.ver, r.err
}

// managerDialer is the set of cluster operations connectSequence needs,
// factored out so the decision flow can be unit tested with a fake.
type managerDialer interface {
	// dialPod connects to pap and returns the outcome.
	dialPod(ctx context.Context, pap *portforward.PodAddress) connectResult
	// discoverPod resolves the traffic-manager Service to a pod.
	discoverPod() (*portforward.PodAddress, error)
	// retryKnownName re-dials pap with backoff, for the forbidden-discovery
	// fallback.
	retryKnownName(ctx context.Context, pap *portforward.PodAddress) connectResult
	// canReachKnownName reports whether the identity may port-forward to the
	// known manager pod.
	canReachKnownName() (bool, error)
}

// clusterDialer is the production managerDialer, binding a Cluster to the
// manager namespace the operations need.
type clusterDialer struct {
	kc        *Cluster
	namespace string
}

func (d clusterDialer) dialPod(ctx context.Context, pap *portforward.PodAddress) connectResult {
	return d.kc.connectToPod(ctx, pap)
}

func (d clusterDialer) discoverPod() (*portforward.PodAddress, error) {
	return portforward.ResolveSvcToPod(d.kc, trafficManagerServiceName, d.namespace, strconv.Itoa(trafficManagerAPIPort))
}

func (d clusterDialer) retryKnownName(ctx context.Context, pap *portforward.PodAddress) connectResult {
	return d.kc.connectKnownNameWithBackoff(ctx, pap)
}

func (d clusterDialer) canReachKnownName() (bool, error) {
	return d.kc.canPortForwardKnownName(d.namespace)
}

// ConnectToManager tries the deterministic StatefulSet pod name directly,
// needing only pods/portforward on that one pod, then falls back to
// Service-based discovery on failure. If discovery is itself forbidden, the
// known-name dial is retried with backoff instead of failing outright, since
// a minimal-RBAC client has no other path to the manager.
func (kc *Cluster) ConnectToManager(dialCtx context.Context, namespace string) (conn *grpc.ClientConn, name string, ver semver.Version, err error) {
	dialCtx, cancel := client.GetConfig(kc).Timeouts().TimeoutContext(dialCtx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	if cc := client.GetConfig(kc).Cluster(); cc.UsesExternalManager() {
		// An external control endpoint is dialed directly: no known-name
		// probe, no service discovery, no port-forward.
		return kc.connectExternal(dialCtx, cc.ManagerAddress)
	}

	knownPap := &portforward.PodAddress{Name: trafficManagerPodName, Namespace: namespace, Port: trafficManagerAPIPort, NoLookup: true}
	return connectSequence(dialCtx, knownNameProbeTimeout, clusterDialer{kc: kc, namespace: namespace}, knownPap).get()
}

// connectSequence implements the known-name / discovery / forbidden-retry
// decision flow over d, so it can be unit tested with a fake dialer.
func connectSequence(
	dialCtx context.Context,
	probeTimeout time.Duration,
	d managerDialer,
	knownPap *portforward.PodAddress,
) connectResult {
	probeCtx, probeCancel := context.WithTimeout(dialCtx, probeTimeout)
	res := d.dialPod(probeCtx, knownPap)
	probeCancel()
	if res.err == nil {
		clog.Debugf(dialCtx, "connected to traffic-manager via known pod name %s", trafficManagerPodName)
		return res
	}
	clog.Debugf(dialCtx, "known-name connect to %s failed, falling back to service discovery: %v", trafficManagerPodName, res.err)

	pap, dErr := d.discoverPod()
	if dErr != nil {
		if k8serrors.IsForbidden(dErr) {
			// Retry only if the identity can actually port-forward to the pod
			// (a minimal-RBAC client has no other fallback); otherwise fail
			// now instead of looping on a retry that cannot succeed.
			if allowed, aErr := d.canReachKnownName(); aErr == nil && !allowed {
				return connectResult{err: knownNameForbiddenError()}
			}
			clog.Debugf(dialCtx, "service discovery forbidden, retrying known-name connect to %s with backoff", trafficManagerPodName)
			return d.retryKnownName(dialCtx, knownPap)
		}
		se := &k8serrors.StatusError{}
		if errors.As(dErr, &se) {
			if se.Status().Code == http.StatusNotFound {
				return connectResult{err: errcat.User.New("traffic manager not found, if it is not installed, please run 'telepresence setup' to configure and install it, " +
					"or 'telepresence helm install' for a plain install. " +
					"If it is installed, try connecting with a --manager-namespace to point telepresence to the namespace it's installed in.")}
			}
		}
		return connectResult{err: dErr}
	}
	clog.Debugf(dialCtx, "connecting to traffic-manager via service discovery, resolved pod %s.%s", pap.Name, pap.Namespace)
	return d.dialPod(dialCtx, pap)
}

// connectKnownNameWithBackoff retries the known-name connect against pap with
// exponential backoff bounded by dialCtx's remaining deadline: reached only
// when discovery is forbidden, so a transient failure must not be fatal.
func (kc *Cluster) connectKnownNameWithBackoff(dialCtx context.Context, pap *portforward.PodAddress) connectResult {
	b := backoff.ExponentialBackOff{
		InitialInterval:     500 * time.Millisecond,
		RandomizationFactor: backoff.DefaultRandomizationFactor,
		Multiplier:          backoff.DefaultMultiplier,
		MaxInterval:         5 * time.Second,
		Stop:                backoff.Stop,
		Clock:               backoff.SystemClock,
	}
	b.Reset()
	var res connectResult
	err := backoff.Retry(func() error {
		res = kc.connectToPod(dialCtx, pap)
		return res.err
	}, backoff.WithContext(&b, dialCtx))
	if err != nil {
		return connectResult{err: knownNameExhaustedError(pap.Name)}
	}
	return res
}

// knownNameExhaustedError means a minimal-RBAC client could reach neither
// the manager pod directly nor the discovery service before the retry
// budget ran out.
func knownNameExhaustedError(podName string) error {
	return errcat.User.Newf(
		"could not reach the traffic-manager pod %q directly, and the current kubeconfig lacks the RBAC to discover it via the %q service; "+
			"if this is a pre-2.33 installation, an admin can enable the legacy discovery grants with the Helm value clientRbac.legacyAccess",
		podName, trafficManagerServiceName)
}

// canPortForwardKnownName uses a SelfSubjectAccessReview, which every
// authenticated identity may make regardless of its other RBAC grants.
func (kc *Cluster) canPortForwardKnownName(namespace string) (bool, error) {
	return k8sapi.CanI(kc, portForwardAttributes(namespace, trafficManagerPodName))
}

// knownNameForbiddenError is the connect refusal for an identity that holds
// neither the discovery RBAC nor pods/portforward on the known manager pod.
func knownNameForbiddenError() error {
	return errcat.User.Newf(
		"forbidden: the current identity may not create pods/portforward for pod %q, nor discover the traffic-manager via the %q service; "+
			"connecting requires one of those grants (see the chart's clientRbac values)",
		trafficManagerPodName, trafficManagerServiceName)
}

// connectToPod dials pap and runs the shared connect handshake. The
// connection is pinned to pap's pod for its lifetime; when the pod goes
// away, the connection dies with it and reconnect resolves a fresh one.
func (kc *Cluster) connectToPod(dialCtx context.Context, pap *portforward.PodAddress) connectResult {
	grpcAddr := pap.AddrFor(pap.Port)

	bearerSrc := newManagerTokenSource(kc.Kubeconfig)
	x509Src := newX509TokenSource(kc.Kubeconfig)
	hasBearerSource := bearerSrc != nil
	hasX509Source := x509Src != nil

	var extra []grpc.DialOption
	switch {
	case hasBearerSource && hasX509Source:
		clog.Debugf(kc, "manager calls will carry the kubeconfig's bearer credentials, falling back to x509 client-certificate credentials")
		extra = append(extra, grpc.WithPerRPCCredentials(newManagerTokenCredentials(&managerAuthTokenSource{bearer: bearerSrc, x509: x509Src}, false)))
	case hasBearerSource:
		clog.Debugf(kc, "manager calls will carry the kubeconfig's bearer credentials")
		extra = append(extra, grpc.WithPerRPCCredentials(newManagerTokenCredentials(bearerSrc, false)))
	case hasX509Source:
		clog.Debugf(kc, "manager calls will carry x509 client-certificate credentials, if the manager supports it")
		extra = append(extra, grpc.WithPerRPCCredentials(newManagerTokenCredentials(x509Src, false)))
	default:
		clog.Debugf(kc, "the kubeconfig yields no bearer token or client certificate for the traffic-manager connection")
	}
	conn, err := kc.dialGRPC(dialCtx, grpcAddr, extra...)
	if err != nil {
		return connectResult{err: err}
	}
	name, ver, err := kc.finishConnect(dialCtx, conn, func(vi *manager.VersionInfo2) error {
		hasX509Path := false
		if hasX509Source {
			if authPort := vi.GetAuthX509Port(); authPort != 0 {
				// The exchange targets the same pinned pod, over the same shared
				// per-pod stream connection as the gRPC channel, so the pod that
				// mints the token is the pod that receives it.
				x509Src.activate(portforward.Dialer(kc), pap.AddrFor(uint16(authPort)))
				hasX509Path = true
				clog.Debugf(kc, "manager calls will carry x509 client-certificate credentials via the manager's auth port %d", authPort)
			}
		}
		if err := managerAuthError(vi, hasBearerSource, hasX509Path); err != nil {
			return err
		}
		if vi.GetAuthSupported() && !vi.GetAuthRequired() && !hasBearerSource && !hasX509Path {
			clog.Debugf(kc, "traffic-manager %s supports authentication, but the current kubeconfig yields no bearer token or usable client certificate", vi.GetName())
		}
		return nil
	})
	return connectResult{conn: conn, name: name, ver: ver, err: err}
}

// finishConnect runs the post-dial handshake shared by both manager
// transports: fetch the manager's version, apply authCheck to it, and parse
// the version number. It closes conn on error and logs the connection
// otherwise.
func (kc *Cluster) finishConnect(
	dialCtx context.Context,
	conn *grpc.ClientConn,
	authCheck func(vi *manager.VersionInfo2) error,
) (name string, ver semver.Version, err error) {
	defer func() {
		if err != nil {
			conn.Close()
		} else {
			clog.Infof(kc, "Connected to Manager %s", ver)
		}
	}()
	vi, err := getVersion(dialCtx, manager.NewManagerClient(conn))
	if err != nil {
		return "", ver, client.CheckTimeout(dialCtx, fmt.Errorf("dial manager: %w", err))
	}
	if err = authCheck(vi); err != nil {
		return "", ver, err
	}
	verStr := strings.TrimPrefix(vi.Version, "v")
	ver, err = semver.Parse(verStr)
	if err != nil {
		err = fmt.Errorf("failed to parse manager version %q: %w", verStr, err)
	}
	return vi.Name, ver, err
}

// connectExternal dials cluster.managerAddress directly, with no Kubernetes
// API calls. It presents exactly one credential in the TLS handshake --
// bearer token if available, otherwise a client certificate -- since the
// external listener rejects a call carrying both.
func (kc *Cluster) connectExternal(dialCtx context.Context, addr string) (conn *grpc.ClientConn, name string, ver semver.Version, err error) {
	hostPort, serverName, err := parseManagerAddress(addr)
	if err != nil {
		return nil, "", ver, err
	}

	bearerSrc := newManagerTokenSource(kc.Kubeconfig)
	hasBearerSource := bearerSrc != nil
	var getClientCert func(*tls.CertificateRequestInfo) (*tls.Certificate, error)
	hasClientCert := false
	if !hasBearerSource {
		if getClientCert = externalClientCertificate(kc.Kubeconfig); getClientCert != nil {
			hasClientCert = true
		}
	}
	creds, err := managerServerCredentials(serverName, client.GetConfig(kc).Cluster().ManagerServerCA, getClientCert)
	if err != nil {
		return nil, "", ver, err
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 24 * time.Hour, Timeout: 20 * time.Second}),
		grpc.WithIdleTimeout(0),
	}
	switch {
	case hasBearerSource:
		clog.Debugf(kc, "manager calls will carry the kubeconfig's bearer credentials")
		opts = append(opts, grpc.WithPerRPCCredentials(newManagerTokenCredentials(bearerSrc, true)))
	case hasClientCert:
		clog.Debugf(kc, "the external traffic-manager connection authenticates with the kubeconfig's client certificate")
	default:
		clog.Debugf(kc, "the kubeconfig yields no bearer token or client certificate for the external traffic-manager connection")
	}

	conn, err = grpcClient.DialGRPC(dialCtx, "dns:///"+hostPort, opts...)
	if err != nil {
		return nil, "", ver, err
	}
	name, ver, err = kc.finishConnect(dialCtx, conn, func(vi *manager.VersionInfo2) error {
		return managerAuthError(vi, hasBearerSource, hasClientCert)
	})
	return conn, name, ver, err
}

// managerAuthError returns a user-facing error when vi reports that the
// manager requires an authenticated client but neither a bearer token nor
// an x509 client-certificate path (hasX509Path: a client certificate plus a
// manager-advertised auth port) is available. It returns nil when no error
// applies.
func managerAuthError(vi *manager.VersionInfo2, hasBearerSource, hasX509Path bool) error {
	if !vi.GetAuthRequired() || hasBearerSource || hasX509Path {
		return nil
	}
	return errcat.User.Newf(
		"traffic-manager %s requires an authenticated client, but the current kubeconfig context's credentials "+
			"cannot produce a bearer token (client-certificate credentials); use a context with token or "+
			"exec-plugin credentials, set the Helm value security.authentication.mode to permissive, or have the "+
			"manager enable x509 client-certificate authentication (Helm value security.authentication.x509.enabled)",
		vi.GetName())
}

type versionAPI interface {
	Version(context.Context, *empty.Empty, ...grpc.CallOption) (*manager.VersionInfo2, error)
}

func (kc *Cluster) ConnectToAgent(
	dialCtx context.Context,
	namespace string,
	podName string,
	port uint16,
	podID types.UID,
) (*grpc.ClientConn, agent.AgentClient, *manager.VersionInfo2, error) {
	pap := portforward.PodAddress{Name: podName, Namespace: namespace, PodID: podID}
	conn, err := kc.dialGRPC(dialCtx, pap.AddrFor(port))
	if err != nil {
		return nil, nil, nil, err
	}
	mClient := agent.NewAgentClient(conn)
	vi, err := getVersion(dialCtx, mClient)
	if err != nil {
		err = client.CheckTimeout(dialCtx, fmt.Errorf("dial agent: %w", err))
		conn.Close()
	}
	return conn, mClient, vi, err
}

func (kc *Cluster) dialGRPC(dialCtx context.Context, address string, extra ...grpc.DialOption) (*grpc.ClientConn, error) {
	opts := append(make([]grpc.DialOption, 0, 5+len(extra)),
		grpc.WithContextDialer(portforward.Dialer(kc)),
		grpc.WithResolvers(portforward.NewResolver(kc)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 5 * time.Minute, Timeout: 20 * time.Second}),
		grpc.WithIdleTimeout(0),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	opts = append(opts, extra...)
	return grpcClient.DialGRPC(dialCtx, portforward.K8sPFScheme+":///"+address, opts...)
}

func getVersion(ctx context.Context, gc versionAPI) (*manager.VersionInfo2, error) {
	// At this point, we are connected to the traffic-manager. We use the shorter API timeout
	tos := client.GetConfig(ctx).Timeouts()
	b := backoff.ExponentialBackOff{
		InitialInterval:     500 * time.Millisecond,
		RandomizationFactor: backoff.DefaultRandomizationFactor,
		Multiplier:          backoff.DefaultMultiplier,
		MaxInterval:         2 * time.Second,
		MaxElapsedTime:      tos.Get(client.TimeoutTrafficManagerAPI),
		Stop:                backoff.Stop,
		Clock:               backoff.SystemClock,
	}
	b.Reset()
	var vi *manager.VersionInfo2
	err := backoff.Retry(func() (err error) {
		vi, err = gc.Version(ctx, &empty.Empty{})
		return err
	}, backoff.WithContext(&b, ctx))
	if err == nil {
		clog.Infof(ctx, "Connected to %s %s", vi.Name, vi.Version)
	}
	return vi, err
}
