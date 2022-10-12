package extensions

import (
	"context"
)

type SystemACredentials string

// GetRequestMetadata implements credentials.PerRPCCredentials.
func (c SystemACredentials) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	md := map[string]string{
		"X-Ambassador-Api-Key": string(c),
	}
	return md, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials.
func (c SystemACredentials) RequireTransportSecurity() bool {
	return true
}
