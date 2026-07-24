package k8s

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/grpc/credentials"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/transport"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator"
)

// managerTokenSource yields the bearer token the kubeconfig's credentials
// resolve to, for presentation to the traffic-manager.
type managerTokenSource interface {
	Token(ctx context.Context) (string, error)
}

// errNoBearerToken indicates that the kubeconfig's credentials yield a client
// certificate rather than a bearer token.
var errNoBearerToken = errors.New("exec credential plugin did not yield a bearer token")

// newManagerTokenSource returns a source for kc's credentials, or nil when the
// kubeconfig cannot produce a bearer token (client-certificate-only credentials).
func newManagerTokenSource(kc *Kubeconfig) managerTokenSource {
	if kc == nil || kc.RestConfig == nil {
		return nil
	}
	rc := kc.RestConfig
	switch {
	case rc.BearerToken != "":
		return staticTokenSource(rc.BearerToken)
	case rc.BearerTokenFile != "":
		return &oauth2TokenSource{ts: transport.NewCachedFileTokenSource(rc.BearerTokenFile)}
	case rc.ExecProvider != nil:
		return newExecTokenSource(rc.ExecProvider)
	case rc.AuthProvider != nil:
		clog.Debugf(kc, "manager bearer token: auth-provider %q is not supported for kubeconfig credentials", rc.AuthProvider.Name)
		return nil
	default:
		return nil
	}
}

// staticTokenSource is a fixed bearer token, e.g. from --token or an
// in-cluster service account.
type staticTokenSource string

func (s staticTokenSource) Token(context.Context) (string, error) {
	return string(s), nil
}

// oauth2TokenSource adapts an oauth2.TokenSource (e.g. a cached file token
// source) to managerTokenSource.
type oauth2TokenSource struct {
	ts oauth2.TokenSource
}

func (o *oauth2TokenSource) Token(context.Context) (string, error) {
	tok, err := o.ts.Token()
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

const (
	// execTokenSafetyMargin is subtracted from a credential's expiration so the
	// token is refreshed slightly before it actually expires.
	execTokenSafetyMargin = time.Minute

	// execTokenDefaultTTL is used to cache a credential that carries no
	// expiration timestamp.
	execTokenDefaultTTL = 10 * time.Minute
)

// execTokenSource runs a kubeconfig exec credential plugin and caches the
// resulting bearer token until it is about to expire.
type execTokenSource struct {
	execConfig *clientcmdapi.ExecConfig

	mu     sync.Mutex
	cached bool
	expiry time.Time
	token  string
	tokErr error
}

// newExecTokenSource returns a source that runs ec to obtain a bearer token.
// ec is copied so the source does not retain pointers into mutable RestConfig
// state.
func newExecTokenSource(ec *clientcmdapi.ExecConfig) *execTokenSource {
	cp := *ec
	cp.Args = append([]string(nil), ec.Args...)
	cp.Env = append([]clientcmdapi.ExecEnvVar(nil), ec.Env...)
	return &execTokenSource{execConfig: &cp}
}

// execCredentialStatus mirrors the status field shared by the
// client.authentication.k8s.io v1 and v1beta1 ExecCredential types.
type execCredentialStatus struct {
	Token               string       `json:"token"`
	ExpirationTimestamp *metav1.Time `json:"expirationTimestamp"`
}

func (e *execTokenSource) Token(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached && time.Now().Before(e.expiry) {
		return e.token, e.tokErr
	}

	out, err := authenticator.ResolveExecConfig(ctx, e.execConfig)
	if err != nil {
		e.cached = false
		return "", err
	}
	var cred struct {
		Status execCredentialStatus `json:"status"`
	}
	if err := json.Unmarshal(out, &cred); err != nil {
		e.cached = false
		return "", fmt.Errorf("unable to parse exec credential: %w", err)
	}

	if cred.Status.ExpirationTimestamp != nil {
		e.expiry = cred.Status.ExpirationTimestamp.Add(-execTokenSafetyMargin)
	} else {
		e.expiry = time.Now().Add(execTokenDefaultTTL)
	}
	e.cached = true
	if cred.Status.Token == "" {
		e.token = ""
		e.tokErr = errNoBearerToken
	} else {
		e.token = cred.Status.Token
		e.tokErr = nil
	}
	return e.token, e.tokErr
}

// managerAuthTokenSource composes a bearer-token source with an x509
// handshake source. The bearer source is tried first; the x509 source is
// consulted when the bearer source is absent, yields no token, or yields
// errNoBearerToken (an exec plugin whose credential carries only a client
// certificate).
type managerAuthTokenSource struct {
	bearer managerTokenSource
	x509   managerTokenSource
}

func (c *managerAuthTokenSource) Token(ctx context.Context) (string, error) {
	if c.bearer != nil {
		token, err := c.bearer.Token(ctx)
		if err == nil && token != "" {
			return token, nil
		}
		if err != nil && !errors.Is(err, errNoBearerToken) {
			return "", err
		}
	}
	if c.x509 != nil {
		return c.x509.Token(ctx)
	}
	return "", nil
}

// managerTokenCredentials is a credentials.PerRPCCredentials that attaches the
// kubeconfig's bearer token to every RPC to the traffic-manager.
type managerTokenCredentials struct {
	source   managerTokenSource
	warnOnce sync.Once
}

var _ credentials.PerRPCCredentials = (*managerTokenCredentials)(nil)

// newManagerTokenCredentials returns credentials backed by source.
func newManagerTokenCredentials(source managerTokenSource) *managerTokenCredentials {
	return &managerTokenCredentials{source: source}
}

// GetRequestMetadata returns the bearer authorization header for the current
// token. A failure to obtain a token (a plugin error, or a plugin that only
// yields a client certificate) is logged once and yields empty metadata
// rather than an error, since the manager is permissive about missing
// tokens. Token contents are never logged.
func (c *managerTokenCredentials) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	token, err := c.source.Token(ctx)
	if err != nil {
		c.warnOnce.Do(func() {
			clog.Warnf(ctx, "unable to obtain a bearer token for the traffic-manager: %v", err)
		})
		return map[string]string{}, nil
	}
	if token == "" {
		return map[string]string{}, nil
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}

// RequireTransportSecurity is false: the connection to the traffic-manager is
// a port-forwarded h2c socket.
func (c *managerTokenCredentials) RequireTransportSecurity() bool {
	return false
}
