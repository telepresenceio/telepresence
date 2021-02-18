package extensions

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/datawire/telepresence2/rpc/v2/common"
	"github.com/datawire/telepresence2/rpc/v2/systema"
	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/client/cache"
)

type systemaCredentials string

// GetRequestMetadata implements credentials.PerRPCCredentials.
func (c systemaCredentials) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	md := map[string]string{
		"Authorization": "Bearer " + string(c),
	}
	return md, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials.
func (c systemaCredentials) RequireTransportSecurity() bool {
	return true
}

func systemaGetPreferredAgentImageName(ctx context.Context, urlStr string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}
	// Discard any path/query/fragment components.
	u = &url.URL{
		Scheme: "dns",
		Host:   u.Host,
	}

	tokenData, err := cache.LoadTokenFromUserCache(ctx)
	if err != nil {
		return "", fmt.Errorf("not logged in: %w", err)
	}
	creds := systemaCredentials(tokenData.AccessToken)

	conn, err := grpc.DialContext(ctx, u.String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{ServerName: u.Hostname()})),
		grpc.WithPerRPCCredentials(creds))
	if err != nil {
		return "", err
	}
	defer conn.Close()

	systemaClient := systema.NewSystemACRUDClient(conn)

	resp, err := systemaClient.PreferredAgent(ctx, &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	})
	if err != nil {
		return "", err
	}

	return resp.GetImageName(), nil
}
