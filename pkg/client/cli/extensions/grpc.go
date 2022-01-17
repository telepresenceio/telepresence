package extensions

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

type SystemaCredentials string

// GetRequestMetadata implements credentials.PerRPCCredentials.
func (c SystemaCredentials) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	md := map[string]string{
		"X-Ambassador-Api-Key": string(c),
	}
	return md, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials.
func (c SystemaCredentials) RequireTransportSecurity() bool {
	return true
}

func systemaGetPreferredAgentImageName(ctx context.Context, urlStr string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}

	apikey, err := cliutil.GetCloudAPIKey(ctx, a8rcloud.KeyDescWorkstation, true)
	if err != nil {
		return "", fmt.Errorf("getting Ambassador Cloud preferred agent image: login error: %w", err)
	}
	creds := SystemaCredentials(apikey)

	conn, err := grpc.DialContext(ctx,
		(&url.URL{Scheme: "dns", Path: "/" + u.Host}).String(), // https://github.com/grpc/grpc/blob/master/doc/naming.md
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{ServerName: u.Hostname()})),
		grpc.WithPerRPCCredentials(creds))
	if err != nil {
		return "", fmt.Errorf("getting Ambassador Cloud preferred agent image: dial error: %w", err)
	}
	defer conn.Close()

	systemaClient := systema.NewSystemACRUDClient(conn)

	resp, err := systemaClient.PreferredAgent(ctx, &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	})
	if err != nil {
		return "", fmt.Errorf("getting Ambassador Cloud preferred agent image: gRPC error: %w", err)
	}

	return resp.GetImageName(), nil
}
