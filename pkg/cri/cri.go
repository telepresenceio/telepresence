//go:build linux

package cri

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// WellKnownSockets are the CRI unix sockets used by common container
// runtimes, in probe order. DetectSocket returns the first one that exists.
var WellKnownSockets = []string{ //nolint:gochecknoglobals // fixed list of well-known paths
	"/run/containerd/containerd.sock",
	"/var/run/crio/crio.sock",
	"/run/k3s/containerd/containerd.sock",
	"/var/run/dockershim.sock",
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

// ResolvePID dials the CRI runtime service at socketPath and returns the
// host PID of containerID's init process.
//
// The PID is not part of the typed ContainerStatus message; runtimes report
// it in the verbose info map returned alongside the status, as a JSON
// document with a top-level "pid" field. This shape is common to both
// containerd and CRI-O.
func ResolvePID(ctx context.Context, socketPath, containerID string) (int, error) {
	conn, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return 0, fmt.Errorf("dial CRI socket %q: %w", socketPath, err)
	}
	defer conn.Close()

	id := StripRuntimePrefix(containerID)
	resp, err := runtimeapi.NewRuntimeServiceClient(conn).ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
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

// DetectSocket returns the first of WellKnownSockets that exists on the
// filesystem. The node-agent normally receives the socket path from its own
// configuration; this is a fallback for when it isn't given one.
func DetectSocket() (string, error) {
	for _, path := range WellKnownSockets {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no CRI socket found among %v", WellKnownSockets)
}
