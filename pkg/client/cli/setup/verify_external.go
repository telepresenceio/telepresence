package setup

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	empty "google.golang.org/protobuf/types/known/emptypb"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
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

// externalProbeStepTimeout bounds each probe step -- long enough for a real
// handshake and RPC, short enough that a silently-dropping firewall cannot
// stall verification for long.
const externalProbeStepTimeout = 5 * time.Second

// externalRestConfigProvider lets a context also yield a REST config, so the
// authenticated-session probe can resolve the kubeconfig's bearer token. When
// ctx does not implement it, the probe is skipped with an explanatory note.
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

// externalProber performs the TLS+gRPC handshake, the Version call, and
// (given a bearer token) an authenticated ArriveAsClient+Depart round trip,
// classifying each step independently.
type externalProber func(ctx context.Context, addr string, caPEM []byte, bearerToken string) externalProbeResult

// verifyExternalEndpoint resolves the published address, waiting briefly for
// a LoadBalancer ingress, then runs the TLS/Version/authenticated-session
// probe. ClusterIP is reported and skipped: it is not externally reachable.
func verifyExternalEndpoint(
	ctx context.Context, ki kubernetes.Interface, managerNamespace string, values *helm.Values, auth ClientAuthFacts, prober externalProber,
) []Note {
	ticker := time.NewTicker(quicLBInterval)
	defer ticker.Stop()

	var note *Note
	var svc *core.Service
	for {
		var kind externalServiceLookKind
		kind, note, svc = externalServiceLook(ctx, ki, managerNamespace)
		if kind != externalServiceProvisioning {
			break
		}
		select {
		case <-ctx.Done():
			return []Note{*note}
		case <-ticker.C:
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

// externalServiceLookKind classifies why externalServiceLook's note is
// terminal, so callers can map it to their own verdict scale without
// matching on the note's text.
type externalServiceLookKind int

const (
	externalServiceReachable externalServiceLookKind = iota
	externalServiceNotFound
	externalServiceReadFailed
	externalServiceProvisioning
	externalServiceNoNodePort
	externalServiceClusterIP
	externalServiceUnsupportedType
)

// externalServiceLook is a single look at the external Service. A non-nil note is
// terminal; a caller with time to spare may look again when kind is
// externalServiceProvisioning (a LoadBalancer whose ingress has not been assigned yet),
// never for any other kind (missing, unreadable, unsupported type, or ClusterIP).
func externalServiceLook(
	ctx context.Context, ki kubernetes.Interface, namespace string,
) (kind externalServiceLookKind, note *Note, svc *core.Service) {
	s, err := ki.CoreV1().Services(namespace).Get(ctx, externalServiceName, meta.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return externalServiceNotFound, &Note{Level: NoteWarning, Text: fmt.Sprintf(
			"the external endpoint service %s was not found in namespace %s: %v", externalServiceName, namespace, err)}, nil
	case err != nil:
		return externalServiceReadFailed, &Note{Level: NoteWarning, Text: fmt.Sprintf(
			"the external endpoint service %s could not be read: %v", externalServiceName, err)}, nil
	}
	switch s.Spec.Type {
	case core.ServiceTypeLoadBalancer:
		if firstLoadBalancerIngressAddr(s) != "" {
			return externalServiceReachable, nil, s
		}
		return externalServiceProvisioning, &Note{Level: NoteWarning, Text: "no external endpoint ingress yet (the LoadBalancer may still be " +
			"provisioning) -- skipping the TLS/gRPC probe"}, nil
	case core.ServiceTypeNodePort:
		if _, ok := firstAllocatedNodePort(s); ok {
			return externalServiceReachable, nil, s
		}
		return externalServiceNoNodePort, &Note{
			Level: NoteWarning,
			Text:  "the external endpoint service has no allocated node port yet -- skipping the TLS/gRPC probe",
		}, nil
	case core.ServiceTypeClusterIP:
		return externalServiceClusterIP, &Note{Level: NoteInfo, Text: fmt.Sprintf(
			"the external endpoint service %s is ClusterIP: it is not reachable from outside the cluster, so setup "+
				"cannot validate it from this workstation; validate it from within the cluster or behind whatever "+
				"ingress/gateway fronts it", externalServiceName)}, nil
	default:
		return externalServiceUnsupportedType, &Note{Level: NoteWarning, Text: fmt.Sprintf(
			"the external endpoint service has type %s; endpoint discovery has nothing externally reachable to probe", s.Spec.Type)}, nil
	}
}

// externalDialAddr picks a LoadBalancer ingress address, or a NodePort plus a
// node address (ExternalIP preferred, InternalIP as fallback).
func externalDialAddr(ctx context.Context, ki kubernetes.Interface, svc *core.Service) (string, error) {
	return resolveServiceDialAddr(ctx, ki, svc, externalPortName, "the external endpoint service")
}

// externalTLSSecretName returns the admin-named Secret, or the chart's fixed
// cert-manager Secret name. Empty when neither is configured (chart render
// would have failed in that case).
func externalTLSSecretName(values *helm.Values) string {
	if name := deref(values.ExternalEndpoint.TLS.SecretName); name != "" {
		return name
	}
	if deref(values.ExternalEndpoint.TLS.CertManager.Enabled) {
		return certManagerSecretName
	}
	return ""
}

// externalCA reads ca.crt from the Secret, falling back to the leaf tls.crt
// (a self-signed or unchained certificate is its own trust anchor). On the
// cert-manager path the Secret may not exist yet, so this waits for it to
// appear, polling every quicLBInterval until ctx is done.
func externalCA(ctx context.Context, ki kubernetes.Interface, namespace string, values *helm.Values) ([]byte, error) {
	name := externalTLSSecretName(values)
	if name == "" {
		return nil, errors.New("externalEndpoint.tls names no Secret (secretName unset and certManager disabled)")
	}
	waitForIssuance := name == certManagerSecretName

	ticker := time.NewTicker(quicLBInterval)
	defer ticker.Stop()
	for {
		sec, err := ki.CoreV1().Secrets(namespace).Get(ctx, name, meta.GetOptions{})
		switch {
		case err == nil:
			if ca := sec.Data["ca.crt"]; len(ca) > 0 {
				return ca, nil
			}
			if leaf := sec.Data["tls.crt"]; len(leaf) > 0 {
				return leaf, nil
			}
			return nil, fmt.Errorf("secret %s has neither ca.crt nor tls.crt", name)
		case !waitForIssuance || !apierrors.IsNotFound(err):
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the cert-manager certificate was not issued within the verification window: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// externalBearerToken resolves rc's bearer-token credential (static token,
// token file, or exec plugin) as a one-shot fetch with no caching -- a single
// verification run only needs it once.
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

// skippedFinding is a VerdictUnknown Finding for a step that never ran
// because an earlier step in the same probe did not succeed.
func skippedFinding(reason string) Finding {
	return Finding{Verdict: VerdictUnknown, Evidence: []string{"skipped: " + reason}}
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

	res := externalProbeResult{
		Version: skippedFinding("the TLS handshake did not succeed"),
		Auth:    skippedFinding("the TLS handshake did not succeed"),
	}

	dctx, cancel := context.WithTimeout(ctx, externalProbeStepTimeout)
	defer cancel()
	conn, err := grpcClient.DialGRPC(dctx, addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConf)))
	if err != nil {
		res.TLS = Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf("TLS handshake to %s failed: %v", addr, err)}}
		return res
	}
	defer conn.Close()
	res.TLS = Finding{Verdict: VerdictYes, Evidence: []string{"TLS handshake to " + addr + " succeeded"}}
	res.Version = skippedFinding("the Version call did not succeed")
	res.Auth = skippedFinding("the Version call did not succeed")

	mc := manager.NewManagerClient(conn)
	vctx, vcancel := context.WithTimeout(ctx, externalProbeStepTimeout)
	defer vcancel()
	vi, err := mc.Version(vctx, &empty.Empty{})
	if err != nil {
		res.Version = Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf("the Version call failed: %v", err)}}
		return res
	}
	res.Version = Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("Version call succeeded (traffic-manager %s)", vi.GetVersion())}}
	res.Auth = skippedFinding("no bearer token available for this probe")

	if bearerToken == "" {
		return res
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
		res.Auth = Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf("the authenticated-session probe (ArriveAsClient) failed: %v", err)}}
		return res
	}
	if _, err = mc.Depart(actx, si); err != nil {
		res.Auth = Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf("the authenticated session was established but Depart failed: %v", err)}}
		return res
	}
	res.Auth = Finding{Verdict: VerdictYes, Evidence: []string{"authenticated-session probe succeeded (ArriveAsClient + Depart)"}}
	return res
}
