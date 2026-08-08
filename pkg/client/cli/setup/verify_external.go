package setup

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	empty "google.golang.org/protobuf/types/known/emptypb"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// externalServiceName is the chart's fixed name for the Service fronting the
// external TLS gRPC listener.
const externalServiceName = "traffic-manager-external"

// externalPortName is the chart's fixed name for the external Service's
// port.
const externalPortName = "external"

// certManagerSecretName is the fixed Secret name the chart's cert-manager
// Certificate issues into (externalEndpoint.tls.certManager.enabled).
const certManagerSecretName = "traffic-manager-external-tls"

// externalProbeStepTimeout bounds each of the TLS dial, Version call, and
// authenticated-session round trip: long enough for a real handshake and
// RPC, short enough that a silently-dropping firewall cannot stall
// verification for long.
const externalProbeStepTimeout = 5 * time.Second

// externalRestConfigProvider is implemented by contexts that can also
// produce a REST config -- concretely *k8s.Cluster, which the setup command
// passes as VerifyInstall's ctx (it embeds *k8s.Kubeconfig, which is both a
// context.Context and exposes GetRestConfig). The authenticated-session
// probe needs it to resolve the kubeconfig's bearer token exactly as a real
// client would. When ctx does not implement it -- a plain context.Context,
// as in most unit tests -- the probe is skipped with an explanatory note.
type externalRestConfigProvider interface {
	GetRestConfig() *rest.Config
}

func restConfigFrom(ctx context.Context) *rest.Config {
	if p, ok := ctx.(externalRestConfigProvider); ok {
		return p.GetRestConfig()
	}
	return nil
}

// externalProbeResult is one full external-endpoint probe: three findings a
// caller renders as separate notes. Auth is always populated, even when
// skipped (no bearer token, or an earlier step failed).
type externalProbeResult struct {
	TLS     Finding
	Version Finding
	Auth    Finding
}

// externalProber performs the TLS+gRPC handshake, the public Version call,
// and (when a bearer token is supplied) an authenticated ArriveAsClient +
// Depart round trip against addr, classifying each step independently.
// Production wires externalGRPCProbe; tests substitute a fake so the
// report-composition logic in verifyExternalEndpoint is exercised without
// opening a real connection.
type externalProber func(ctx context.Context, addr string, caPEM []byte, bearerToken string) externalProbeResult

// verifyExternalEndpoint mirrors verifyQuic's shape for the external TLS
// control endpoint: it resolves the published address (waiting briefly for
// a LoadBalancer ingress the same way verifyQuic does), then runs the
// TLS/Version/authenticated-session probe. A ClusterIP Service is reported
// and skipped rather than probed: it is not reachable from outside the
// cluster at all.
func verifyExternalEndpoint(
	ctx context.Context, ki kubernetes.Interface, managerNamespace string, values map[string]any, auth ClientAuthFacts, prober externalProber,
) []Note {
	note, retryLB, svc := externalServiceLook(ctx, ki, managerNamespace)
	if retryLB {
		ticker := time.NewTicker(quicLBInterval)
		defer ticker.Stop()
		for retryLB {
			select {
			case <-ctx.Done():
				return []Note{*note}
			case <-ticker.C:
			}
			note, retryLB, svc = externalServiceLook(ctx, ki, managerNamespace)
		}
	}
	if note != nil {
		return []Note{*note}
	}

	addr, err := externalDialAddr(ctx, ki, svc)
	if err != nil {
		return []Note{{Level: NoteWarning, Text: fmt.Sprintf(
			"the external endpoint exists but its dial address could not be determined: %v", err)}}
	}

	var notes []Note
	caPEM, caErr := externalCA(ctx, ki, managerNamespace, values)
	if caErr != nil {
		notes = append(notes, Note{Level: NoteWarning, Text: fmt.Sprintf(
			"could not read the external listener's CA to verify its TLS certificate (%v); the probe will use the system trust store", caErr)})
	}

	var bearerToken string
	if auth.Bearer {
		tok, err := externalBearerToken(ctx, restConfigFrom(ctx))
		if err != nil {
			notes = append(notes, Note{Level: NoteWarning, Text: fmt.Sprintf(
				"could not obtain this kubeconfig's bearer token for the authenticated-session probe: %v", err)})
		} else {
			bearerToken = tok
		}
	}

	result := prober(ctx, addr, caPEM, bearerToken)
	notes = append(notes, noteFromFinding(result.TLS), noteFromFinding(result.Version))
	if result.Version.Verdict == VerdictYes {
		notes = append(notes, clientConfigNote(addr, caPEM))
	}
	notes = append(notes, noteFromFinding(result.Auth))
	return notes
}

// externalServiceLook is a single look at the traffic-manager-external
// Service. A non-nil note is terminal: nothing more to check yet (a
// LoadBalancer or NodePort not provisioned, retryLB says whether it is
// worth looking again), or ever (the Service is missing, unreadable, of an
// unsupported type, or ClusterIP -- not externally reachable by design).
func externalServiceLook(ctx context.Context, ki kubernetes.Interface, namespace string) (note *Note, retryLB bool, svc *corev1.Service) {
	s, err := ki.CoreV1().Services(namespace).Get(ctx, externalServiceName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return &Note{Level: NoteWarning, Text: fmt.Sprintf(
			"the external endpoint service %s was not found in namespace %s: %v", externalServiceName, namespace, err)}, false, nil
	case err != nil:
		return &Note{Level: NoteWarning, Text: fmt.Sprintf(
			"the external endpoint service %s could not be read: %v", externalServiceName, err)}, false, nil
	}
	switch s.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		for _, ing := range s.Status.LoadBalancer.Ingress {
			if ing.IP != "" || ing.Hostname != "" {
				return nil, false, s
			}
		}
		return &Note{Level: NoteWarning, Text: "no external endpoint ingress yet (the LoadBalancer may still be " +
			"provisioning) -- skipping the TLS/gRPC probe"}, true, nil
	case corev1.ServiceTypeNodePort:
		for _, p := range s.Spec.Ports {
			if p.NodePort != 0 {
				return nil, false, s
			}
		}
		return &Note{Level: NoteWarning, Text: "the external endpoint service has no allocated node port yet -- skipping the TLS/gRPC probe"}, false, nil
	case corev1.ServiceTypeClusterIP:
		return &Note{Level: NoteInfo, Text: fmt.Sprintf(
			"the external endpoint service %s is ClusterIP: it is not reachable from outside the cluster, so setup "+
				"cannot validate it from this workstation; validate it from within the cluster or behind whatever "+
				"ingress/gateway fronts it", externalServiceName)}, false, nil
	default:
		return &Note{Level: NoteWarning, Text: fmt.Sprintf(
			"the external endpoint service has type %s; endpoint discovery has nothing externally reachable to probe", s.Spec.Type)}, false, nil
	}
}

// externalDialAddr resolves the address to dial for the probe from the
// external Service's already-confirmed endpoint: a LoadBalancer's ingress
// address, or a NodePort together with a node address (ExternalIP
// preferred, InternalIP as fallback, via the QUIC probe's firstNodeAddress).
func externalDialAddr(ctx context.Context, ki kubernetes.Interface, svc *corev1.Service) (string, error) {
	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		port, ok := externalServicePort(svc)
		if !ok {
			return "", errors.New("the external endpoint service has no identifiable external port")
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			addr := ing.IP
			if addr == "" {
				addr = ing.Hostname
			}
			if addr != "" {
				return net.JoinHostPort(addr, strconv.Itoa(int(port.Port))), nil
			}
		}
		return "", errors.New("the external endpoint service has no assigned LoadBalancer ingress")
	case corev1.ServiceTypeNodePort:
		port, ok := externalServicePort(svc)
		if !ok || port.NodePort == 0 {
			return "", errors.New("the external endpoint service has no allocated node port")
		}
		addr, err := firstNodeAddress(ctx, ki)
		if err != nil {
			return "", err
		}
		return net.JoinHostPort(addr, strconv.Itoa(int(port.NodePort))), nil
	default:
		return "", fmt.Errorf("service type %s has no externally reachable address", svc.Spec.Type)
	}
}

// externalServicePort finds the chart's "external" port on svc, falling
// back to the sole port when the Service carries exactly one.
func externalServicePort(svc *corev1.Service) (corev1.ServicePort, bool) {
	for _, p := range svc.Spec.Ports {
		if p.Name == externalPortName {
			return p, true
		}
	}
	if len(svc.Spec.Ports) == 1 {
		return svc.Spec.Ports[0], true
	}
	return corev1.ServicePort{}, false
}

// externalTLSSecretName names the Secret the external listener's
// certificate lives in, per the values' externalEndpoint.tls setting: the
// admin-named Secret, or the chart's fixed cert-manager Secret name. Empty
// when neither is configured (chart render would have failed in that case).
func externalTLSSecretName(values map[string]any) string {
	if v, ok := valueAt(values, "externalEndpoint", "tls", "secretName"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if enabled, _ := boolAt(values, "externalEndpoint", "tls", "certManager", "enabled"); enabled {
		return certManagerSecretName
	}
	return ""
}

// externalCA fetches the PEM CA that signs the external listener's
// certificate, from the Secret externalTLSSecretName names: ca.crt when the
// Secret carries one, the leaf tls.crt otherwise (a self-signed or
// otherwise unchained certificate is its own trust anchor). Reading the
// Secret uses the same admin credentials the rest of setup runs with.
func externalCA(ctx context.Context, ki kubernetes.Interface, namespace string, values map[string]any) ([]byte, error) {
	name := externalTLSSecretName(values)
	if name == "" {
		return nil, errors.New("externalEndpoint.tls names no Secret (secretName unset and certManager disabled)")
	}
	sec, err := ki.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if ca := sec.Data["ca.crt"]; len(ca) > 0 {
		return ca, nil
	}
	if leaf := sec.Data["tls.crt"]; len(leaf) > 0 {
		return leaf, nil
	}
	return nil, fmt.Errorf("secret %s has neither ca.crt nor tls.crt", name)
}

// externalBearerToken resolves rc's bearer-token credential the same way a
// connecting client would: a static token, a token file, or an exec
// credential plugin. It is a one-shot fetch with no caching, since a single
// verification run only needs the token once -- unlike the client's
// long-lived per-RPC credentials in pkg/client/k8s.
func externalBearerToken(ctx context.Context, rc *rest.Config) (string, error) {
	switch {
	case rc == nil:
		return "", errors.New("no REST config available")
	case rc.BearerToken != "":
		return rc.BearerToken, nil
	case rc.BearerTokenFile != "":
		data, err := os.ReadFile(rc.BearerTokenFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	case rc.ExecProvider != nil:
		out, err := authenticator.ResolveExecConfig(ctx, rc.ExecProvider)
		if err != nil {
			return "", err
		}
		var cred struct {
			Status struct {
				Token string `json:"token"`
			} `json:"status"`
		}
		if err := json.Unmarshal(out, &cred); err != nil {
			return "", fmt.Errorf("unable to parse exec credential: %w", err)
		}
		if cred.Status.Token == "" {
			return "", errors.New("exec credential plugin did not yield a bearer token")
		}
		return cred.Status.Token, nil
	default:
		return "", errors.New("this kubeconfig produces no bearer token")
	}
}

// clientConfigNote renders the ready-to-paste client config block for the
// endpoint the probe just confirmed reachable over TLS/gRPC.
func clientConfigNote(addr string, caPEM []byte) Note {
	caLine := "<not obtained; see the warning above -- fetch it from the external listener's TLS Secret and base64-encode ca.crt or tls.crt>"
	if len(caPEM) > 0 {
		caLine = base64.StdEncoding.EncodeToString(caPEM)
	}
	text := fmt.Sprintf("client config for this endpoint:\ncluster:\n  managerAddress: tls://%s\n  managerServerCA: %s", addr, caLine)
	return Note{Level: NoteInfo, Text: text}
}

// externalGRPCProbe is the production externalProber: a real TLS dial, a
// real Version call, and -- when bearerToken is non-empty -- a real
// ArriveAsClient/Depart round trip.
func externalGRPCProbe(ctx context.Context, addr string, caPEM []byte, bearerToken string) externalProbeResult {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(caPEM) {
			tlsConf.RootCAs = pool
		}
	}

	dctx, cancel := context.WithTimeout(ctx, externalProbeStepTimeout)
	defer cancel()
	conn, err := grpcClient.DialGRPC(dctx, addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConf)))
	if err != nil {
		return externalProbeResult{
			TLS:     Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf("TLS handshake to %s failed: %v", addr, err)}},
			Version: Finding{Verdict: VerdictUnknown, Evidence: []string{"skipped: the TLS handshake did not succeed"}},
			Auth:    Finding{Verdict: VerdictUnknown, Evidence: []string{"skipped: the TLS handshake did not succeed"}},
		}
	}
	defer conn.Close()
	tlsFinding := Finding{Verdict: VerdictYes, Evidence: []string{"TLS handshake to " + addr + " succeeded"}}

	mc := manager.NewManagerClient(conn)
	vctx, vcancel := context.WithTimeout(ctx, externalProbeStepTimeout)
	defer vcancel()
	vi, err := mc.Version(vctx, &empty.Empty{})
	if err != nil {
		return externalProbeResult{
			TLS:     tlsFinding,
			Version: Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf("the Version call failed: %v", err)}},
			Auth:    Finding{Verdict: VerdictUnknown, Evidence: []string{"skipped: the Version call did not succeed"}},
		}
	}
	versionFinding := Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("Version call succeeded (traffic-manager %s)", vi.GetVersion())}}

	if bearerToken == "" {
		return externalProbeResult{TLS: tlsFinding, Version: versionFinding, Auth: Finding{
			Verdict: VerdictUnknown, Evidence: []string{"skipped: no bearer token available for this probe"},
		}}
	}

	actx, acancel := context.WithTimeout(ctx, externalProbeStepTimeout)
	defer acancel()
	actx = metadata.AppendToOutgoingContext(actx, "authorization", "Bearer "+bearerToken)
	si, err := mc.ArriveAsClient(actx, &manager.ClientInfo{
		Name:      "telepresence setup",
		InstallId: "telepresence-setup-verify",
		Product:   "telepresence",
		Version:   version.Version,
	})
	if err != nil {
		return externalProbeResult{TLS: tlsFinding, Version: versionFinding, Auth: Finding{
			Verdict: VerdictNo, Evidence: []string{fmt.Sprintf("the authenticated-session probe (ArriveAsClient) failed: %v", err)},
		}}
	}
	if _, err = mc.Depart(actx, si); err != nil {
		return externalProbeResult{TLS: tlsFinding, Version: versionFinding, Auth: Finding{
			Verdict: VerdictNo, Evidence: []string{fmt.Sprintf("the authenticated session was established but Depart failed: %v", err)},
		}}
	}
	return externalProbeResult{TLS: tlsFinding, Version: versionFinding, Auth: Finding{
		Verdict: VerdictYes, Evidence: []string{"authenticated-session probe succeeded (ArriveAsClient + Depart)"},
	}}
}
