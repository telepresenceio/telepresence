package k8s

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestClassifyUnreachable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "dns lookup failure",
			err:  fmt.Errorf(`Get "https://x.example:443/version": dial tcp: %w`, &net.DNSError{Err: "no such host", Name: "x.example", IsNotFound: true}),
			want: "could not be resolved",
		},
		{
			name: "unknown certificate authority",
			err:  fmt.Errorf("tls: %w", x509.UnknownAuthorityError{}),
			want: "TLS certificate could not be verified",
		},
		{
			name: "unauthorized",
			err:  k8serrors.NewUnauthorized("token expired"),
			want: "authentication was rejected",
		},
		{
			name: "forbidden",
			err:  k8serrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("nope")),
			want: "access was forbidden",
		},
		{
			name: "connection refused",
			err:  errors.New(`Get "https://x:443/version": dial tcp 10.0.0.1:443: connect: connection refused`),
			want: "refused the connection",
		},
		{
			name: "timeout",
			err:  errors.New(`Get "https://x:443/version": net/http: request canceled (Client.Timeout exceeded while awaiting headers)`),
			want: "timed out",
		},
		{
			name: "no route to host",
			err:  errors.New(`dial tcp 10.0.0.1:443: connect: no route to host`),
			want: "no network route",
		},
		{
			name: "unrecognized falls back to raw error",
			err:  errors.New("something entirely unexpected"),
			want: "something entirely unexpected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyUnreachable(tt.err)
			if !strings.Contains(got, tt.want) {
				t.Errorf("classifyUnreachable() = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}
