package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/client"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type clientKey struct{}

func EnableClient(ctx context.Context) (context.Context, error) {
	if ctx.Value(clientKey{}) != nil {
		return ctx, nil
	}
	cmd := proc.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	stdout, err := proc.CaptureErr(ctx, cmd)
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if err != nil {
		return ctx, fmt.Errorf("unable to retrieve docker context: %v", err)
	}
	if host := strings.TrimSpace(string(stdout)); host != "" {
		dlog.Debugf(ctx, "Using docker host %q", host)
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, clientKey{}, cli), nil
}

func GetClient(ctx context.Context) *client.Client {
	if cli, ok := ctx.Value(clientKey{}).(*client.Client); ok {
		return cli
	}
	panic("docker client not initialized")
}
