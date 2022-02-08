package userd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/userdaemon"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
)

func (s *service) ResolveIngressInfo(ctx context.Context, req *userdaemon.IngressInfoRequest) (*userdaemon.IngressInfoResponse, error) {
	conn, err := ConnectSessionToSystemA(ctx, s.session)
	if err != nil {
		return nil, err
	}

	systemacli := userdaemon.NewSystemAClient(conn)

	return systemacli.ResolveIngressInfo(ctx, req)
}

type systemaCredentials string

// GetRequestMetadata implements credentials.PerRPCCredentials
func (c systemaCredentials) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	md := map[string]string{
		"X-Ambassador-Api-Key": string(c),
	}
	return md, nil
}

func (c systemaCredentials) RequireTransportSecurity() bool {
	return true
}

// ConnectSystemA tries to create a connection to the given systemaURL
// using the apiKey for authorization and returns the connection if it
// was successful in creating one
func ConnectSystemA(ctx context.Context, systemaURL, apiKey string) (*grpc.ClientConn, error) {
	u, err := url.Parse(systemaURL)
	if err != nil {
		dlog.Errorf(ctx, "error parsing url: %s", err)
		return nil, err
	}
	creds := systemaCredentials(apiKey)
	conn, err := grpc.DialContext(ctx,
		(&url.URL{Scheme: "dns", Path: "/" + u.Host}).String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{ServerName: u.Hostname()})),
		grpc.WithPerRPCCredentials(creds))
	if err != nil {
		dlog.Errorf(ctx, "error establishing gRPC connection: %s", err)
		return nil, err
	}
	return conn, nil
}

func ConnectSessionToSystemA(ctx context.Context, session trafficmgr.Session) (*grpc.ClientConn, error) {
	managerClient := session.ManagerClient()
	cloudConfig, err := managerClient.GetCloudConfig(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, fmt.Errorf("unable to get cloud config: %w", err)
	}
	apiKey, err := cliutil.GetCloudAPIKey(ctx, a8rcloud.KeyDescWorkstation, true)
	if err != nil {
		return nil, fmt.Errorf("error logging in to Ambassador Cloud: %w", err)
	}
	cloudAddr := "https://" + cloudConfig.GetHost() + ":" + cloudConfig.GetPort()
	return ConnectSystemA(ctx, cloudAddr, apiKey)
}
