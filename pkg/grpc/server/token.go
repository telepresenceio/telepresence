package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TokenMetadataKey is the gRPC metadata key that carries a daemon's secret.
const TokenMetadataKey = "x-telepresence-token"

// NewToken returns a new random secret, hex encoded.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// TokenInterceptors returns interceptors that reject calls that don't carry the given token in the
// TokenMetadataKey metadata. Calls to the methods named by exempt, given as full method names, need no token.
// An empty token is never accepted.
func TokenInterceptors(token string, exempt ...string) *Interceptors {
	check := func(ctx context.Context, fullMethod string) error {
		if slices.Contains(exempt, fullMethod) {
			return nil
		}
		if token != "" {
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				for _, v := range md.Get(TokenMetadataKey) {
					if subtle.ConstantTimeCompare([]byte(v), []byte(token)) == 1 {
						return nil
					}
				}
			}
		}
		return status.Error(codes.Unauthenticated, "missing or invalid token")
	}
	return &Interceptors{
		Unary: func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if err := check(ctx, info.FullMethod); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		},
		Stream: func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if err := check(ss.Context(), info.FullMethod); err != nil {
				return err
			}
			return handler(srv, ss)
		},
	}
}
