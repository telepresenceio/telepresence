package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// externalEndpointSpecX509Disabled is externalEndpointSpec with
// security.authentication.x509.enabled=false, under its own spec Key.
func externalEndpointSpecX509Disabled(secretName string) managers.Spec {
	off := false
	v := managers.Merge(externalEndpointSpec(secretName).Values, managers.Values{
		Security: managers.Security{Authentication: managers.Authentication{X509: managers.X509{Enabled: &off}}},
	})
	return managers.Spec{Key: "external-endpoint-x509-disabled", Values: v}
}

// ExternalEndpointX509Disabled proves that with x509 authentication switched
// off, the external listener never asks a client for a certificate, while
// bearer-token authentication on it is unchanged.
type ExternalEndpointX509Disabled struct {
	rt.Suite
}

func init() {
	rt.Register(&ExternalEndpointX509Disabled{}, rt.InArea("auth"), rt.NeedsManager(managers.Default))
}

func (s *ExternalEndpointX509Disabled) Test_ExternalEndpointRequestsNoClientCertificate() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()

	nodeIP, nodePort, caPath := setupExternalEndpointSpec(t, ctx, r, externalEndpointSpecX509Disabled)

	caPEM, err := os.ReadFile(caPath)
	s.Require().NoError(err)
	pool := x509.NewCertPool()
	s.Require().True(pool.AppendCertsFromPEM(caPEM), "CA pem should parse")

	// GetClientCertificate runs only when the server sends a certificate request.
	var certRequested atomic.Bool
	tlsCfg := &tls.Config{
		RootCAs: pool,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			certRequested.Store(true)
			return &tls.Certificate{}, nil
		},
	}

	target := net.JoinHostPort(nodeIP, strconv.Itoa(nodePort))
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := grpcClient.DialGRPC(dialCtx, target, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	s.Require().NoError(err, "dialing the external endpoint")
	t.Cleanup(func() { conn.Close() })
	mc := manager.NewManagerClient(conn)

	// Version forces the TLS handshake.
	_, err = mc.Version(ctx, &empty.Empty{})
	s.Require().NoError(err, "Version should be reachable")
	s.False(certRequested.Load(), "the external listener must not request a client certificate when x509 authentication is disabled")

	// Bearer-token authentication is unchanged.
	name := createGrantIdentity(t, ctx, r, "rtest-external-x509-off", telepresenceGrantRules)
	tok := kubectlCreateToken(t, ctx, r, name)
	tokCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	si, err := mc.ArriveAsClient(tokCtx, &manager.ClientInfo{
		Name:      name,
		Namespace: ns,
		InstallId: name,
		Product:   "telepresence",
		Version:   "v" + r.Version().String(),
	})
	s.Require().NoError(err, "ArriveAsClient with a bearer token over the external endpoint")
	t.Cleanup(func() { _, _ = mc.Depart(tokCtx, si) })
	s.False(certRequested.Load(), "no client certificate should be requested on any connection")
}
