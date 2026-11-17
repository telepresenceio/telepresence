package client

import (
	"context"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

type tokenCredentials string

func (t tokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{server.TokenMetadataKey: string(t)}, nil
}

func (tokenCredentials) RequireTransportSecurity() bool {
	return false
}

// WithToken returns a dial option that sends the token with every unary and streaming call.
func WithToken(token string) grpc.DialOption {
	return grpc.WithPerRPCCredentials(tokenCredentials(token))
}
