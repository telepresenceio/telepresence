package manager

import (
	"context"
	stderrors "errors"
	"net"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const serviceAccountUserPrefix = "system:serviceaccount:"

// requireAgentAudience rejects, in enforcing mode, a caller whose token did
// not pass review with the traffic-manager audience.
func (s *service) requireAgentAudience(ctx context.Context) error {
	if s.authMode != auth.ModeEnforcing {
		return nil
	}
	if p := auth.PrincipalFrom(ctx); p == nil || !p.ManagerAudience {
		return errors.Errorf(codes.PermissionDenied, "agent calls require a token with the traffic-manager audience")
	}
	return nil
}

// verifyAgentPod checks, in enforcing mode, that the pod named by the verified
// principal exists with the principal's UID and is a traffic-agent pod of the
// workload that agent claims.
func (s *service) verifyAgentPod(ctx context.Context, p *auth.Principal, agent *rpc.AgentInfo) error {
	if s.authMode != auth.ModeEnforcing {
		return nil
	}
	if p == nil || !p.ManagerAudience {
		return errors.Errorf(codes.PermissionDenied, "agent calls require a token with the traffic-manager audience")
	}
	rest, ok := strings.CutPrefix(p.Username, serviceAccountUserPrefix)
	if !ok {
		return errors.Errorf(codes.PermissionDenied, "agent token does not belong to a service account")
	}
	podNamespace, _, _ := strings.Cut(rest, ":")
	pod, err := lookupPod(ctx, podNamespace, p.PodName)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			return agentIdentityDenied(ctx, agent, "pod %s.%s does not exist", p.PodName, podNamespace)
		}
		return errors.Errorf(codes.Unavailable, "unable to look up pod %s.%s: %v", p.PodName, podNamespace, err)
	}
	if string(pod.UID) != p.PodUID {
		return agentIdentityDenied(ctx, agent, "pod %s.%s has a different UID", p.PodName, podNamespace)
	}
	if !slices.ContainsFunc(pod.Status.PodIPs, func(ip core.PodIP) bool { return ip.IP == agent.PodIp }) && pod.Status.PodIP != agent.PodIp {
		return agentIdentityDenied(ctx, agent, "pod IP %s is not an address of pod %s.%s", agent.PodIp, p.PodName, podNamespace)
	}
	if agent.NodeAgent {
		return s.verifyNodeAgentPod(ctx, pod, agent)
	}
	if !slices.ContainsFunc(pod.Spec.Containers, func(c core.Container) bool { return c.Name == agentconfig.ContainerName }) {
		return agentIdentityDenied(ctx, agent, "pod %s.%s has no %s container", p.PodName, podNamespace, agentconfig.ContainerName)
	}
	wl, err := agentmap.FindOwnerWorkload(ctx, k8sapi.Pod(pod), managerutil.GetEnv(ctx).EnabledWorkloadKinds)
	if err != nil {
		clog.Debugf(ctx, "unable to resolve the owner workload of pod %s.%s: %v", pod.Name, pod.Namespace, err)
		if lookupFailed(err) {
			return errors.Errorf(codes.Unavailable, "unable to resolve the owner of pod %s.%s: %v", p.PodName, podNamespace, err)
		}
		return agentIdentityDenied(ctx, agent, "pod %s.%s has no resolvable workload owner", p.PodName, podNamespace)
	}
	if wl.GetName() != agent.Name || string(wl.GetKind()) != agent.Kind {
		return agentIdentityDenied(ctx, agent, "pod %s.%s is owned by %s, not by %s %s", p.PodName, podNamespace, wl, agent.Kind, agent.Name)
	}
	if !mutator.PodControlledBy(ctx, pod, wl) {
		return agentIdentityDenied(ctx, agent, "the owner references of pod %s.%s do not lead to %s", p.PodName, podNamespace, wl)
	}
	return nil
}

// verifyNodeAgentPod requires pod to belong to a Job that the traffic-manager
// created for the claimed agent name and workload namespace.
func (s *service) verifyNodeAgentPod(ctx context.Context, pod *core.Pod, agent *rpc.AgentInfo) error {
	if !state.IsNodeAgentJobFor(pod.Labels, agent.Name, agent.Namespace) {
		return agentIdentityDenied(ctx, agent, "pod %s.%s is not a node-agent pod for this workload", pod.Name, pod.Namespace)
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Controller == nil || !*ref.Controller || ref.Kind != "Job" {
			continue
		}
		job, err := k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(pod.Namespace).Get(ctx, ref.Name, meta.GetOptions{})
		if err != nil {
			if k8sErrors.IsNotFound(err) {
				break
			}
			return errors.Errorf(codes.Unavailable, "unable to look up job %s.%s: %v", ref.Name, pod.Namespace, err)
		}
		if job.UID == ref.UID && state.IsNodeAgentJobFor(job.Labels, agent.Name, agent.Namespace) {
			return nil
		}
		break
	}
	return agentIdentityDenied(ctx, agent, "pod %s.%s does not belong to a node-agent job for this workload", pod.Name, pod.Namespace)
}

// lookupFailed reports whether err is a failed API call (timeout, server
// error, transport error) rather than the absence of an owner workload.
func lookupFailed(err error) bool {
	if k8sErrors.IsNotFound(err) {
		return false
	}
	var st k8sErrors.APIStatus
	var ne net.Error
	return stderrors.As(err, &st) || stderrors.As(err, &ne) ||
		stderrors.Is(err, context.DeadlineExceeded) || stderrors.Is(err, context.Canceled)
}

func agentIdentityDenied(ctx context.Context, agent *rpc.AgentInfo, format string, args ...any) error {
	clog.Warnf(ctx, "rejecting agent %s.%s: "+format, append([]any{agent.PodName, agent.Namespace}, args...)...)
	return errors.Errorf(codes.PermissionDenied, "agent identity %s.%s does not match the cluster", agent.PodName, agent.Namespace)
}

// lookupPod reads the pod from the informer cache and falls back to the API
// server when the cache has not seen it.
func lookupPod(ctx context.Context, namespace, name string) (*core.Pod, error) {
	if f := informer.GetK8sFactory(ctx, namespace); f != nil {
		pod, err := f.Core().V1().Pods().Lister().Pods(namespace).Get(name)
		if err == nil {
			return pod, nil
		}
		if !k8sErrors.IsNotFound(err) {
			return nil, err
		}
	}
	return k8sapi.GetK8sInterface(ctx).CoreV1().Pods(namespace).Get(ctx, name, meta.GetOptions{})
}
