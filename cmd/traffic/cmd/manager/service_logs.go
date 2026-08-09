package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// Fallback values for when an Env's LogStream* field is non-positive (e.g.
// an Env built directly rather than via managerutil.LoadEnv, as in tests).
// They match the Helm chart's own defaults.
const (
	defaultLogStreamChunkSize      = 64 * 1024
	defaultLogStreamPodConcurrency = 4
	defaultLogStreamPodByteLimit   = 10 * 1024 * 1024
	defaultLogStreamDeadline       = 5 * time.Minute
)

// logTarget identifies one pod StreamLogs reads from and the container whose
// log to read.
type logTarget struct {
	podName      string
	podNamespace string
	container    string
}

// logChunkSender serializes LogChunk sends onto the response stream: several
// pods are read concurrently, and grpc.ServerStreamingServer.Send is not
// safe for concurrent use.
type logChunkSender struct {
	mu     sync.Mutex
	stream grpc.ServerStreamingServer[rpc.LogChunk]
}

func (s *logChunkSender) send(chunk *rpc.LogChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(chunk)
}

func (s *logChunkSender) sendError(target logTarget, msg string) error {
	return s.send(&rpc.LogChunk{
		PodName:      target.podName,
		PodNamespace: target.podNamespace,
		Payload:      &rpc.LogChunk_Error{Error: msg},
	})
}

// logNamespaceAuth memoizes the per-namespace log SubjectAccessReview
// outcome for one StreamLogs request. An Unavailable outcome is never
// cached, so a later pod in the same namespace retries instead of being
// stuck on a transient failure.
type logNamespaceAuth struct {
	authorizer *auth.Authorizer
	principal  *auth.Principal

	mu   sync.Mutex
	logs map[string]error
	yaml map[string]error
}

func newLogNamespaceAuth(authorizer *auth.Authorizer, principal *auth.Principal) *logNamespaceAuth {
	return &logNamespaceAuth{
		authorizer: authorizer,
		principal:  principal,
		logs:       make(map[string]error),
		yaml:       make(map[string]error),
	}
}

// canGetLogs returns nil when the principal may get logs.telepresence.io in
// namespace, a PermissionDenied error when it may not, and an Unavailable
// error when the review could not be performed.
func (c *logNamespaceAuth) canGetLogs(ctx context.Context, namespace string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err, ok := c.logs[namespace]; ok {
		return err
	}
	allowed, sarErr := c.authorizer.CanGetLogs(ctx, c.principal, namespace)
	if sarErr != nil {
		return errors.Errorf(codes.Unavailable,
			"unable to determine whether %s may get logs.telepresence.io in namespace %s: %v", c.principal.Username, namespace, sarErr)
	}
	var result error
	if !allowed {
		result = errors.Errorf(codes.PermissionDenied,
			"%s is not permitted to get logs.telepresence.io in namespace %s", c.principal.Username, namespace)
	}
	c.logs[namespace] = result
	return result
}

// canGetLogsYAML is canGetLogs for the yaml subresource, authorizing
// inclusion of a pod's manifest.
func (c *logNamespaceAuth) canGetLogsYAML(ctx context.Context, namespace string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err, ok := c.yaml[namespace]; ok {
		return err
	}
	allowed, sarErr := c.authorizer.CanGetLogsYAML(ctx, c.principal, namespace)
	if sarErr != nil {
		return errors.Errorf(codes.Unavailable,
			"unable to determine whether %s may get logs/yaml.telepresence.io in namespace %s: %v", c.principal.Username, namespace, sarErr)
	}
	var result error
	if !allowed {
		result = errors.Errorf(codes.PermissionDenied,
			"%s is not permitted to get logs/yaml.telepresence.io in namespace %s", c.principal.Username, namespace)
	}
	c.yaml[namespace] = result
	return result
}

// StreamLogs streams the traffic-manager's own log and/or its traffic-agents'
// logs to the caller, framed per pod: BEGIN, data chunks, an optional
// trailing error frame, and END, with the pod's manifest sent before END
// when requested and authorized separately. Unlike every other call site in
// this file, a nil principal is refused with Unauthenticated in every
// authentication mode -- this endpoint's entire payload is pod contents and
// diagnostic data, so an unproven caller must not trigger it. A denied or
// unreviewable namespace does not abort the request: its pods still get
// BEGIN/END, with an error frame naming the denial in between.
func (s *service) StreamLogs(request *rpc.StreamLogsRequest, stream grpc.ServerStreamingServer[rpc.LogChunk]) error {
	ctx, session, err := s.ensureClientSession(stream.Context(), request.GetSession())
	if err != nil {
		return err
	}

	principal := auth.PrincipalFrom(ctx)
	if principal == nil {
		return errors.Errorf(codes.Unauthenticated, "streaming logs requires an authenticated caller")
	}

	env := managerutil.GetEnv(ctx)
	release, err := session.BeginLogStream()
	if err != nil {
		return err
	}
	defer release()

	deadline := env.LogStreamDeadline
	if deadline <= 0 {
		deadline = defaultLogStreamDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	targets, err := logStreamTargets(ctx, request)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}

	concurrency := env.LogStreamPodConcurrency
	if concurrency <= 0 {
		concurrency = defaultLogStreamPodConcurrency
	}

	sender := &logChunkSender{stream: stream}
	nsAuth := newLogNamespaceAuth(s.authorizer, principal)

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)
	for _, target := range targets {
		g.Go(func() error {
			return streamPodLog(gCtx, sender, nsAuth, target, request.GetGetPodYaml(), env)
		})
	}
	return g.Wait()
}

// logStreamTargets resolves a StreamLogsRequest's traffic_manager and agents
// selection into the concrete pods to stream from.
func logStreamTargets(ctx context.Context, request *rpc.StreamLogsRequest) ([]logTarget, error) {
	var targets []logTarget
	if request.GetTrafficManager() {
		podName, err := os.Hostname()
		if err != nil {
			return nil, errors.Errorf(codes.Unavailable, "unable to determine this pod's name: %v", err)
		}
		targets = append(targets, logTarget{
			podName:      podName,
			podNamespace: managerutil.GetEnv(ctx).ManagerNamespace,
			container:    agentconfig.ManagerAppName,
		})
	}

	agentsSel := request.GetAgents()
	if agentsSel != "" && !strings.EqualFold(agentsSel, "false") {
		agentTargets, err := agentPodTargets(ctx, agentsSel)
		if err != nil {
			return nil, err
		}
		targets = append(targets, agentTargets...)
	}
	return targets, nil
}

// agentPodTargets enumerates traffic-agent pods via the shared pod informer,
// filtered by agentsSel ("all" or a pod-name substring). The informer (not
// AgentSession state) also surfaces an agent that crashed or never
// completed ArriveAsAgent.
func agentPodTargets(ctx context.Context, agentsSel string) ([]logTarget, error) {
	all := strings.EqualFold(agentsSel, "all")

	var targets []logTarget
	for _, ns := range namespaces.GetOrGlobal(ctx) {
		lister := informer.GetK8sFactory(ctx, ns).Core().V1().Pods().Lister()
		var pods []*corev1.Pod
		var err error
		if ns != "" {
			pods, err = lister.Pods(ns).List(labels.Everything())
		} else {
			pods, err = lister.List(labels.Everything())
		}
		if err != nil {
			return nil, errors.Errorf(codes.Unavailable, "unable to list pods in namespace %q: %v", ns, err)
		}
		for _, pod := range pods {
			if agentmap.AgentContainer(pod) == nil {
				continue
			}
			if !all && !strings.Contains(pod.Name, agentsSel) {
				continue
			}
			targets = append(targets, logTarget{
				podName:      pod.Name,
				podNamespace: pod.Namespace,
				container:    agentconfig.ContainerName,
			})
		}
	}
	return targets, nil
}

// streamPodLog sends the full per-pod frame sequence for target. It returns
// a non-nil error only when the stream itself fails -- every other failure
// becomes an error frame so one pod's trouble does not abort the others.
func streamPodLog(ctx context.Context, sender *logChunkSender, nsAuth *logNamespaceAuth, target logTarget, wantYAML bool, env *managerutil.Env) error {
	if err := sender.send(&rpc.LogChunk{PodName: target.podName, PodNamespace: target.podNamespace, Frame: rpc.LogChunk_BEGIN}); err != nil {
		return err
	}

	if authErr := nsAuth.canGetLogs(ctx, target.podNamespace); authErr != nil {
		if err := sender.sendError(target, authErr.Error()); err != nil {
			return err
		}
		return sender.send(&rpc.LogChunk{PodName: target.podName, PodNamespace: target.podNamespace, Frame: rpc.LogChunk_END})
	}

	if err := readPodLog(ctx, sender, target, env); err != nil {
		return err
	}

	if wantYAML {
		// A denied logs/yaml review omits the manifest without an error frame:
		// the caller's grant simply doesn't include manifests. An Unavailable
		// review outcome is still reported.
		switch authErr := nsAuth.canGetLogsYAML(ctx, target.podNamespace); {
		case authErr == nil:
			if err := sendPodYAML(ctx, sender, target); err != nil {
				return err
			}
		case status.Code(authErr) != codes.PermissionDenied:
			if err := sender.sendError(target, authErr.Error()); err != nil {
				return err
			}
		}
	}

	return sender.send(&rpc.LogChunk{PodName: target.podName, PodNamespace: target.podNamespace, Frame: rpc.LogChunk_END})
}

// readPodLog reads target's log in chunks up to the configured per-pod byte
// cap. A read failure or truncation is reported as a trailing error frame,
// not a return error -- only a broken response stream is.
func readPodLog(ctx context.Context, sender *logChunkSender, target logTarget, env *managerutil.Env) error {
	req := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(target.podNamespace).GetLogs(target.podName, &corev1.PodLogOptions{
		Container: target.container,
	})
	rc, err := req.Stream(ctx)
	if err != nil {
		return sender.sendError(target, fmt.Sprintf("failed to read log: %v", err))
	}
	defer rc.Close()

	chunkSize := env.LogStreamChunkSize.Value()
	if chunkSize <= 0 {
		chunkSize = defaultLogStreamChunkSize
	}
	byteLimit := env.LogStreamPodByteLimit.Value()
	if byteLimit <= 0 {
		byteLimit = defaultLogStreamPodByteLimit
	}

	buf := make([]byte, chunkSize)
	var total int64
	for {
		n, readErr := rc.Read(buf)
		if n > 0 {
			data := buf[:n]
			if remaining := byteLimit - total; int64(n) > remaining {
				if remaining > 0 {
					if err := sender.send(&rpc.LogChunk{
						PodName: target.podName, PodNamespace: target.podNamespace,
						Payload: &rpc.LogChunk_Data{Data: append([]byte(nil), data[:remaining]...)},
					}); err != nil {
						return err
					}
				}
				return sender.sendError(target, fmt.Sprintf("log truncated at the %d-byte per-pod cap", byteLimit))
			}
			if err := sender.send(&rpc.LogChunk{
				PodName: target.podName, PodNamespace: target.podNamespace,
				Payload: &rpc.LogChunk_Data{Data: append([]byte(nil), data...)},
			}); err != nil {
				return err
			}
			total += int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return sender.sendError(target, fmt.Sprintf("log read failed: %v", readErr))
		}
	}
}

// sendPodYAML sends target's current pod manifest as a pod_yaml frame.
func sendPodYAML(ctx context.Context, sender *logChunkSender, target logTarget) error {
	pod, err := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(target.podNamespace).Get(ctx, target.podName, metav1.GetOptions{})
	if err != nil {
		return sender.sendError(target, fmt.Sprintf("failed to get pod manifest: %v", err))
	}
	b, err := yaml.Marshal(pod)
	if err != nil {
		return sender.sendError(target, fmt.Sprintf("failed to marshal pod manifest: %v", err))
	}
	return sender.send(&rpc.LogChunk{
		PodName: target.podName, PodNamespace: target.podNamespace,
		Payload: &rpc.LogChunk_PodYaml{PodYaml: b},
	})
}
