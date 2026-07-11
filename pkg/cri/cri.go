//go:build linux

package cri

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// WellKnownSockets are the CRI unix sockets used by common container
// runtimes, in probe order, all rooted under /run (modern hosts make
// /var/run a symlink to /run). DetectSocket returns the first one that
// answers a CRI Version request.
var WellKnownSockets = []string{ //nolint:gochecknoglobals // fixed list of well-known paths
	"/run/containerd/containerd.sock",
	"/run/crio/crio.sock",
	"/run/k3s/containerd/containerd.sock",
	"/run/cri-dockerd.sock",
	"/run/dockershim.sock",
}

// StripRuntimePrefix removes the "<runtime>://" scheme that Kubernetes
// prepends to container IDs (e.g. "containerd://abc123"). Container IDs
// without a "://" separator are returned unchanged.
func StripRuntimePrefix(containerID string) string {
	if _, id, ok := strings.Cut(containerID, "://"); ok {
		return id
	}
	return containerID
}

// Client is a connection to a CRI runtime service. It is reused across
// calls to ResolvePID so that resolving the PIDs of several containers on
// the same socket dials only once.
type Client struct {
	conn *grpc.ClientConn
	rt   runtimeapi.RuntimeServiceClient
}

// Connect creates a Client for the CRI runtime service at socketPath.
// grpc.NewClient does not dial eagerly, so no context is needed here;
// connection errors surface from the first RPC made through the Client.
func Connect(socketPath string) (*Client, error) {
	conn, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial CRI socket %q: %w", socketPath, err)
	}
	return &Client{conn: conn, rt: runtimeapi.NewRuntimeServiceClient(conn)}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// ResolvePID returns the host PID of containerID's init process.
//
// The PID is not part of the typed ContainerStatus message; runtimes report
// it in the verbose info map returned alongside the status, as a JSON
// document with a top-level "pid" field. This shape is common to both
// containerd and CRI-O.
func (c *Client) ResolvePID(ctx context.Context, containerID string) (int, error) {
	id := StripRuntimePrefix(containerID)
	resp, err := c.rt.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
		ContainerId: id,
		Verbose:     true,
	})
	if err != nil {
		return 0, fmt.Errorf("get status of container %q: %w", id, err)
	}

	raw, ok := resp.GetInfo()["info"]
	if !ok {
		return 0, fmt.Errorf(`container %q: CRI response has no verbose "info" entry`, id)
	}
	var info struct {
		Pid int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return 0, fmt.Errorf("container %q: parse verbose info JSON: %w", id, err)
	}
	if info.Pid <= 0 {
		return 0, fmt.Errorf("container %q: verbose info has no pid", id)
	}
	return info.Pid, nil
}

// preferredSockets maps the runtime scheme that Kubernetes prepends to a
// container ID (e.g. "docker://abc123") to the sockets of the runtimes that
// mint such IDs, giving detection a deterministic first guess.
var preferredSockets = map[string][]string{ //nolint:gochecknoglobals // fixed prefix-to-socket mapping
	"containerd": {"/run/containerd/containerd.sock", "/run/k3s/containerd/containerd.sock"},
	"cri-o":      {"/run/crio/crio.sock"},
	"docker":     {"/run/cri-dockerd.sock", "/run/dockershim.sock"},
}

// SocketFor returns the CRI socket that can resolve containerID, probing the
// WellKnownSockets under root (the path where the node's /run is mounted, or
// empty when running directly on the node). Sockets matching the container
// ID's runtime scheme are probed first.
//
// A socket qualifies only when it answers a ContainerStatus request for the
// container itself. Anything weaker misidentifies the runtime: a
// docker-runtime node can run a containerd whose CRI service is fully alive,
// while kubelet's containers exist only in cri-dockerd.
func SocketFor(ctx context.Context, root, containerID string) (string, error) {
	scheme, _, _ := strings.Cut(containerID, "://")
	candidates := slices.Clone(preferredSockets[scheme])
	for _, path := range WellKnownSockets {
		if !slices.Contains(candidates, path) {
			candidates = append(candidates, path)
		}
	}

	var probeErrs []string
	for _, path := range candidates {
		sp := path
		if root != "" {
			sp = filepath.Join(root, strings.TrimPrefix(path, "/run/"))
		}
		if fi, err := os.Stat(sp); err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		if err := probeContainer(ctx, sp, containerID); err != nil {
			probeErrs = append(probeErrs, fmt.Sprintf("%s: %v", sp, err))
			continue
		}
		return sp, nil
	}
	if len(probeErrs) > 0 {
		return "", fmt.Errorf("no CRI socket recognizes container %q: %s", containerID, strings.Join(probeErrs, "; "))
	}
	return "", fmt.Errorf("no CRI socket found among %v under root %q", WellKnownSockets, root)
}

// probeContainer issues a ContainerStatus request for containerID with a
// short deadline to verify that the socket serves the CRI runtime service
// that actually manages the container.
func probeContainer(ctx context.Context, socketPath, containerID string) error {
	c, err := Connect(socketPath)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = c.rt.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
		ContainerId: StripRuntimePrefix(containerID),
	})
	return err
}
