package docker

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/docker/docker/client"

	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type clientKey struct{}

type clientHandle struct {
	sync.Mutex
	cli *client.Client
}

func (h *clientHandle) GetClient(ctx context.Context) (*client.Client, error) {
	h.Lock()
	defer h.Unlock()
	if h.cli == nil {
		cmd := proc.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		stdout, err := proc.CaptureErr(cmd)
		opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
		if err != nil {
			return nil, fmt.Errorf("unable to retrieve docker context: %v", err)
		}
		if host := strings.TrimSpace(string(stdout)); host != "" {
			opts = append(opts, client.WithHost(host))
		}
		cli, err := client.NewClientWithOpts(opts...)
		if err != nil {
			return nil, err
		}
		h.cli = cli
	}
	return h.cli, nil
}

func EnableClient(ctx context.Context) context.Context {
	if ctx.Value(clientKey{}) == nil {
		ctx = context.WithValue(ctx, clientKey{}, &clientHandle{})
	}
	return ctx
}

func GetClient(ctx context.Context) (*client.Client, error) {
	if h, ok := ctx.Value(clientKey{}).(*clientHandle); ok {
		return h.GetClient(ctx)
	}
	panic("docker client not initialized")
}
