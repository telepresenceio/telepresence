//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/cri"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/procfs"
	"github.com/telepresenceio/telepresence/v2/pkg/sigctx"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	// envNodeAgentContainerIDs holds a JSON object mapping agent container name to CRI
	// container ID, e.g. {"app":"containerd://abc"}. The traffic-manager populates it.
	envNodeAgentContainerIDs = "_TEL_NODE_AGENT_CONTAINER_IDS"

	// envNodeAgentCRISocket is the CRI unix socket path. When unset, cri.DetectSocket is used.
	envNodeAgentCRISocket = "_TEL_NODE_AGENT_CRI_SOCKET"
)

// nodeConfig is the sidecar Config, augmented with the host PID that each configured
// container resolves to on this node. It satisfies the full Config interface through the
// embedded *config and overrides AppEnviron to source the environment from procfs instead
// of the node-agent's own process.
type nodeConfig struct {
	*config
	pids map[string]int
}

// AppEnviron reads the environment of the container's target process from procfs and applies
// the same transformation the sidecar applies to its own process environment.
func (c *nodeConfig) AppEnviron(_ context.Context, cn *agentconfig.Container) (map[string]string, error) {
	pid, ok := c.pids[cn.Name]
	if !ok {
		return nil, fmt.Errorf("no resolved PID for container %q", cn.Name)
	}
	env, err := procfs.Environ(pid)
	if err != nil {
		return nil, err
	}
	return appEnvironment(dos.MapEnv(env).Environ(), cn), nil
}

// parseContainerIDs decodes the JSON object mapping container name to CRI container ID.
func parseContainerIDs(raw string) (map[string]string, error) {
	var ids map[string]string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", envNodeAgentContainerIDs, err)
	}
	return ids, nil
}

// loadNodeConfig loads the sidecar AGENT_CONFIG and pod identity like the sidecar agent, then
// resolves each configured container's host PID via the CRI socket and exports its remote
// mounts from procfs. Unlike LoadConfig, it does not run the sidecar-only addAppMounts step.
func loadNodeConfig(ctx context.Context) (*nodeConfig, error) {
	base, err := loadBaseConfig(ctx)
	if err != nil {
		return nil, err
	}

	raw, ok := dos.LookupEnv(ctx, envNodeAgentContainerIDs)
	if !ok {
		return nil, fmt.Errorf("missing %s", envNodeAgentContainerIDs)
	}
	ids, err := parseContainerIDs(raw)
	if err != nil {
		return nil, err
	}

	socket, ok := dos.LookupEnv(ctx, envNodeAgentCRISocket)
	if !ok || socket == "" {
		if socket, err = cri.DetectSocket(); err != nil {
			return nil, err
		}
	}

	sc := base.AgentConfig()
	pids := make(map[string]int, len(sc.Containers))
	for _, cn := range sc.Containers {
		id, ok := ids[cn.Name]
		if !ok {
			return nil, fmt.Errorf("no container ID for container %q in %s", cn.Name, envNodeAgentContainerIDs)
		}
		pid, err := cri.ResolvePID(ctx, socket, id)
		if err != nil {
			return nil, fmt.Errorf("resolve PID for container %q: %w", cn.Name, err)
		}
		pids[cn.Name] = pid
	}

	for _, cn := range sc.Containers {
		if err := exportProcMounts(ctx, agentconfig.ExportsMountPoint, pids[cn.Name], cn); err != nil {
			return nil, err
		}
	}

	return &nodeConfig{config: base, pids: pids}, nil
}

// exportProcMounts populates exportsRoot/<base of cn.MountPoint> with symlinks into pid's
// mount namespace, one per remote mount path declared in cn.Mounts. The FTP client strips
// the ExportsMountPoint prefix from the reported MountPoint (a client-side contract that is
// out of scope here), so serving must stay rooted under exportsRoot rather than exposing
// /proc/<pid>/root directly; the symlinks resolve in the target's mount namespace when the
// node-agent's ftp/sftp server follows them.
func exportProcMounts(ctx context.Context, exportsRoot string, pid int, cn *agentconfig.Container) error {
	clog.Infof(ctx, "Exporting procfs mounts for container %s", cn.Name)
	cnMountPoint := filepath.Join(exportsRoot, filepath.Base(cn.MountPoint))
	if err := dos.Mkdir(ctx, cnMountPoint, 0o700); err != nil {
		if !os.IsExist(err) {
			return err
		}
		if err := dos.RemoveAll(ctx, cnMountPoint); err != nil {
			return err
		}
		if err := dos.Mkdir(ctx, cnMountPoint, 0o700); err != nil {
			return err
		}
	}

	for path, policy := range cn.Mounts {
		switch policy {
		case types.MountPolicyRemote, types.MountPolicyRemoteReadOnly:
		default:
			continue
		}
		target := filepath.Join(cnMountPoint, path)
		if err := dos.MkdirAll(ctx, filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := dos.Symlink(ctx, procfs.RootPath(pid, path), target); err != nil {
			return err
		}
	}
	return nil
}

// NodeAgentMain is the entrypoint for the node-agent Job pod. It mirrors Main, but loads its
// config from procfs/CRI-resolved target identity instead of its own process environment and
// filesystem, and runs NodeAgentSidecar instead of Sidecar.
func NodeAgentMain(ctx context.Context, _ ...string) error {
	debug.SetTraceback("single")
	clog.Infof(ctx, "Traffic Agent (node) %s", version.Version)

	return sigctx.DoWithSignalHandler(ctx, func(ctx context.Context) error {
		cfg, err := loadNodeConfig(ctx)
		if err != nil {
			return fmt.Errorf("unable to load config: %w", err)
		}

		g := log.NewGroup(ctx)
		s, err := NewState(ctx, cfg)
		if err != nil {
			return err
		}
		info, err := StartServices(g, cfg, s)
		if err != nil {
			return err
		}

		certsReadyCh := make(chan struct{})
		s.TLSManager().StartWatchers(g, certsReadyCh)

		g.Go("node-agent", func(ctx context.Context) error {
			<-certsReadyCh
			return NodeAgentSidecar(g, s, info)
		})

		// Wait for exit
		return g.Wait()
	})
}

// NodeAgentSidecar registers each container's env and mount info like Sidecar, but installs
// no port handlers: forwarders must bind in the target network namespace, which requires a
// setns layer that is deferred to the netns/interception milestone.
func NodeAgentSidecar(g log.Group, s State, info *rpc.AgentInfo) error {
	ac := s.AgentConfig()
	for _, cn := range ac.Containers {
		ci := info.Containers[cn.Name]
		cs := s.NewContainerState(s, cn, ci.MountPoint, ci.Environment)
		s.AddContainerState(cn.Name, cs)
	}
	TalkToManagerLoop(g, s, info)
	return nil
}
