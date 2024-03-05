package state

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
	events "k8s.io/api/events/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	managerrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

// PrepareIntercept ensures that the given request can be matched against the intercept configuration of
// the workload that it references. It returns a PreparedIntercept where all intercepted ports have been
// qualified with a service name and a service port name.
//
// The first step is to find the requested Workload and the agent config for that workload. This step will
// create the initial ConfigMap for the namespace if it doesn't exist yet, and also generate the actual
// intercept config if it doesn't exist.
//
// The second step matches all ServicePortIdentifiers in the request to the intercepts of the agent config
// and creates a resulting PreparedIntercept with a services array that has the same size and positions as
// the ServicePortIdentifiers in the request.
//
// It's expected that the client that makes the call will update any unqualified service port identifiers
// with the ones in the returned PreparedIntercept.
func (s *state) PrepareIntercept(
	ctx context.Context,
	cr *managerrpc.CreateInterceptRequest,
) (pi *managerrpc.PreparedIntercept, err error) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "state.PrepareIntercept")
	defer tracing.EndAndRecord(span, err)
	span.SetAttributes(attribute.Stringer("request", cr))

	interceptError := func(err error) (*managerrpc.PreparedIntercept, error) {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return &managerrpc.PreparedIntercept{Error: err.Error(), ErrorCategory: int32(errcat.GetCategory(err))}, nil
	}

	spec := cr.InterceptSpec
	wl, err := agentmap.GetWorkload(ctx, spec.Agent, spec.Namespace, spec.WorkloadKind)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			err = errcat.User.New(err)
		}
		return interceptError(err)
	}

	ac, err := s.ensureAgent(ctx, wl, s.isExtended(spec), spec)
	if err != nil {
		return interceptError(err)
	}
	_, ic, err := findIntercept(ac, spec)
	if err != nil {
		return interceptError(err)
	}
	return &managerrpc.PreparedIntercept{
		Namespace:       ac.Namespace,
		ServiceUid:      string(ic.ServiceUID),
		ServiceName:     ic.ServiceName,
		ServicePortName: ic.ServicePortName,
		ServicePort:     int32(ic.ServicePort),
		AgentImage:      ac.AgentImage,
		WorkloadKind:    ac.WorkloadKind,
	}, nil
}

func (s *state) EnsureAgent(ctx context.Context, n, ns string) error {
	wl, err := agentmap.GetWorkload(ctx, n, ns, "")
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			err = errcat.User.New(err)
		}
		return err
	}
	_, err = s.ensureAgent(ctx, wl, false, nil)
	return err
}

func (s *state) ValidateCreateAgent(context.Context, k8sapi.Workload, agentconfig.SidecarExt) error {
	return nil
}

func (s *state) ensureAgent(parentCtx context.Context, wl k8sapi.Workload, extended bool, spec *managerrpc.InterceptSpec) (*agentconfig.Sidecar, error) {
	ctx, cancel := context.WithTimeout(parentCtx, managerutil.GetEnv(parentCtx).AgentArrivalTimeout)
	defer cancel()

	failedCreateCh, err := watchFailedInjectionEvents(ctx, wl.GetName(), wl.GetNamespace())
	if err != nil {
		return nil, err
	}

	sce, err := s.getOrCreateAgentConfig(ctx, wl, extended, spec)
	if err != nil {
		return nil, err
	}
	ac := sce.AgentConfig()
	if err = s.waitForAgent(ctx, ac.AgentName, ac.Namespace, failedCreateCh); err != nil {
		// If no agent arrives, then drop its entry from the configmap. This ensures that there
		// are no false positives the next time an intercept is attempted.
		if dropErr := s.dropAgentConfig(parentCtx, wl); dropErr != nil {
			dlog.Errorf(ctx, "failed to remove configmap entry for %s.%s: %v", wl.GetName(), wl.GetNamespace(), dropErr)
		}
		return nil, err
	}
	return ac, nil
}

func (s *state) isExtended(spec *managerrpc.InterceptSpec) bool {
	return spec.Mechanism != "tcp"
}

func (s *state) ValidateAgentImage(agentImage string, extended bool) (err error) {
	if agentImage == "" {
		err = errcat.User.Newf(
			"intercepts are disabled because the traffic-manager is unable to determine what image to use for injected traffic-agents.")
	} else if extended {
		err = errcat.User.New("traffic-manager does not support intercepts that require an extended traffic-agent")
	}
	return err
}

func (s *state) dropAgentConfig(
	ctx context.Context,
	wl k8sapi.Workload,
) error {
	return mutator.GetMap(ctx).Delete(ctx, wl.GetNamespace(), wl.GetName())
}

func (s *state) RestoreAppContainer(ctx context.Context, ii *managerrpc.InterceptInfo) (err error) {
	dlog.Debugf(ctx, "Restoring app container for %s", ii.Id)
	spec := ii.Spec
	n := spec.Agent
	ns := spec.Namespace
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "state.RestoreAppContainer", trace.WithAttributes(
		attribute.String("tel2.name", n),
		attribute.String("tel2.namespace", ns),
	))
	defer func() {
		tracing.EndAndRecord(span, err)
	}()
	return mutator.GetMap(ctx).Update(ctx, ns, func(cm *core.ConfigMap) (changed bool, err error) {
		y, ok := cm.Data[n]
		if !ok {
			return false, nil
		}
		sce, err := unmarshalConfigMapEntry(y, n, ns)
		if err != nil {
			return false, err
		}
		cn, _, err := findIntercept(sce.AgentConfig(), spec)
		if !(err == nil && cn.Replace) {
			return false, nil
		}
		cn.Replace = false

		// The pods for this workload will be killed once the new updated sidecar
		// reaches the configmap. We remove them now, so that they don't continue to
		// review intercepts.
		for sessionID := range s.getAgentsByName(n, ns) {
			if as, ok := s.GetSession(sessionID).(*agentSessionState); ok {
				as.active.Store(false)
			}
		}
		return updateSidecar(sce, cm, n)
	})
}

func updateSidecar(sce agentconfig.SidecarExt, cm *core.ConfigMap, n string) (bool, error) {
	yml, err := sce.Marshal()
	if err != nil {
		return false, err
	}
	oldYaml := cm.Data[n]
	newYaml := string(yml)
	if oldYaml != newYaml {
		cm.Data[n] = newYaml
		return true, nil
	}
	return false, nil
}

func (s *state) getOrCreateAgentConfig(
	ctx context.Context,
	wl k8sapi.Workload,
	extended bool,
	spec *managerrpc.InterceptSpec,
) (sce agentconfig.SidecarExt, err error) {
	enabled, err := checkInterceptAnnotations(wl)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errcat.User.Newf("%s %s.%s is not interceptable", wl.GetKind(), wl.GetName(), wl.GetNamespace())
	}

	agentImage := managerutil.GetAgentImage(ctx)
	if err = s.self.ValidateAgentImage(agentImage, extended); err != nil {
		return nil, err
	}
	err = mutator.GetMap(ctx).Update(ctx, wl.GetNamespace(), func(cm *core.ConfigMap) (changed bool, err error) {
		doUpdate := false
		y, cmFound := cm.Data[wl.GetName()]
		if cmFound {
			if sce, err = unmarshalConfigMapEntry(y, wl.GetName(), wl.GetNamespace()); err != nil {
				return false, err
			}
			ac := sce.AgentConfig()
			// If the agentImage has changed, and the extended image is requested, then update
			if ac.AgentImage != agentImage && extended {
				ac.AgentImage = agentImage
				doUpdate = true
			}
		} else {
			if cm.Data == nil {
				cm.Data = make(map[string]string)
			}
			var gc agentmap.GeneratorConfig
			if gc, err = agentmap.GeneratorConfigFunc(agentImage); err != nil {
				return false, err
			}
			if sce, err = gc.Generate(ctx, wl, nil); err != nil {
				return false, err
			}
			doUpdate = true
		}

		ac := sce.AgentConfig()
		if spec != nil {
			cn, _, err := findIntercept(ac, spec)
			if err != nil {
				return false, err
			}
			if cn.Replace != agentconfig.ReplacePolicy(spec.Replace) {
				cn.Replace = agentconfig.ReplacePolicy(spec.Replace)
				doUpdate = true
			}
		}
		if doUpdate {
			if cmFound {
				// The pods for this workload be killed once the new updated sidecar
				// reaches the configmap. We remove them now, so that they don't continue to
				// review intercepts.
				for sessionID := range s.getAgentsByName(wl.GetName(), wl.GetNamespace()) {
					if as, ok := s.GetSession(sessionID).(*agentSessionState); ok {
						as.active.Store(false)
					}
				}
			} else {
				if err = s.self.ValidateCreateAgent(ctx, wl, sce); err != nil {
					return false, err
				}
			}
			return updateSidecar(sce, cm, wl.GetName())
		}
		return false, nil
	})
	return sce, err
}

func checkInterceptAnnotations(wl k8sapi.Workload) (bool, error) {
	pod := wl.GetPodTemplate()
	a := pod.Annotations
	if a == nil {
		return true, nil
	}

	webhookEnabled := true
	manuallyManaged := a[mutator.ManualInjectAnnotation] == "true"
	ia := a[mutator.InjectAnnotation]
	switch ia {
	case "":
		webhookEnabled = !manuallyManaged
	case "enabled":
	case "false", "disabled":
		webhookEnabled = false
	default:
		return false, errcat.User.Newf(
			"%s is not a valid value for the %s.%s/%s annotation",
			ia, wl.GetName(), wl.GetNamespace(), mutator.ManualInjectAnnotation)
	}

	if !manuallyManaged {
		return webhookEnabled, nil
	}
	cns := pod.Spec.Containers
	var an *core.Container
	for i := range cns {
		cn := &cns[i]
		if cn.Name == agentconfig.ContainerName {
			an = cn
			break
		}
	}
	if an == nil {
		return false, errcat.User.Newf(
			"annotation %s.%s/%s=true but pod has no traffic-agent container",
			wl.GetName(), wl.GetNamespace(), mutator.ManualInjectAnnotation)
	}
	return true, nil
}

func watchFailedInjectionEvents(ctx context.Context, name, namespace string) (<-chan *events.Event, error) {
	// A timestamp with second granularity is needed here, because that's what the event creation time uses.
	// Finer granularity will result in relevant events seemingly being created before this timestamp because
	// they have the fraction of seconds trimmed off (which is odd, given that the type used is a MicroTime).
	start := time.Unix(time.Now().Unix(), 0)

	ei := k8sapi.GetK8sInterface(ctx).EventsV1().Events(namespace)
	w, err := ei.Watch(ctx, meta.ListOptions{
		FieldSelector: fields.OneTermNotEqualSelector("type", "Normal").String(),
	})
	if err != nil {
		return nil, err
	}
	nd := name + "-"
	ec := make(chan *events.Event)
	go func() {
		defer w.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case eo, ok := <-w.ResultChan():
				if !ok {
					return
				}
				// Using a negated Before when comparing the timestamps here is relevant. They will often be equal and still relevant
				if e, ok := eo.Object.(*events.Event); ok &&
					!e.CreationTimestamp.Time.Before(start) &&
					!strings.HasPrefix(e.Note, "(combined from similar events):") {
					n := e.Regarding.Name
					if strings.HasPrefix(n, nd) || n == name {
						dlog.Infof(ctx, "%s %s %s", e.Type, e.Reason, e.Note)
						ec <- e
					}
				}
			}
		}
	}()
	return ec, nil
}

func (s *state) waitForAgent(ctx context.Context, name, namespace string, failedCreateCh <-chan *events.Event) error {
	snapshotCh := s.WatchAgents(ctx, nil)
	failedContainerRx := regexp.MustCompile(`restarting failed container (\S+) in pod ([0-9A-Za-z_-]+)_` + namespace)
	// fes collects events from the failedCreatedCh and is included in the error message in case
	// the waitForAgent call times out.
	var fes []*events.Event
	for {
		select {
		case fe, ok := <-failedCreateCh:
			if ok {
				msg := fe.Note
				// Terminate directly on known fatal events. No need for the user to wait for a timeout
				// when one of these are encountered.
				switch fe.Reason {
				case "BackOff":
					// The traffic-agent container was injected, but it fails to start
					if rr := failedContainerRx.FindStringSubmatch(msg); rr != nil {
						cn := rr[1]
						pod := rr[2]
						rq := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(namespace).GetLogs(pod, &core.PodLogOptions{
							Container: cn,
						})
						if rs, err := rq.Stream(ctx); err == nil {
							if log, err := io.ReadAll(rs); err == nil {
								dlog.Infof(ctx, "Log from failing pod %q, container %s\n%s", pod, cn, string(log))
							} else {
								dlog.Errorf(ctx, "failed to read log stream from pod %q, container %s\n%s", pod, cn, err)
							}
							_ = rs.Close()
						} else {
							dlog.Errorf(ctx, "failed to read log from pod %q, container %s\n%s", pod, cn, err)
						}
					}
					msg = fmt.Sprintf("%s\nThe logs of %s %s might provide more details", msg, fe.Regarding.Kind, fe.Regarding.Name)
				case "Failed", "FailedCreate", "FailedScheduling":
					// The injection of the traffic-agent failed for some reason, most likely due to resource quota restrictions.
					if fe.Type == "Warning" && (strings.Contains(msg, "waiting for ephemeral volume") ||
						strings.Contains(msg, "unbound immediate PersistentVolumeClaims") ||
						strings.Contains(msg, "skip schedule deleting pod")) {
						// This isn't fatal.
						fes = append(fes, fe)
						continue
					}
					msg = fmt.Sprintf(
						"%s\nHint: if the error mentions resource quota, the traffic-agent's requested resources can be configured by providing values to telepresence helm install",
						msg)
				default:
					// Something went wrong, but it might not be fatal. There are several events logged that are just
					// warnings where the action will be retried and eventually succeed.
					fes = append(fes, fe)
					continue
				}
				return errcat.User.New(msg)
			}
		case snapshot, ok := <-snapshotCh:
			if !ok {
				// The request has been canceled.
				return status.Error(codes.Canceled, fmt.Sprintf("channel closed while waiting for agent %s.%s to arrive", name, namespace))
			}
			for _, a := range snapshot.State {
				if a.Namespace == namespace && a.Name == name {
					return nil
				}
			}
		case <-ctx.Done():
			v := "canceled"
			if ctx.Err() == context.DeadlineExceeded {
				v = "timed out"
			}
			bf := &strings.Builder{}
			fmt.Fprintf(bf, "request %s while waiting for agent %s.%s to arrive", v, name, namespace)
			if len(fes) > 0 {
				bf.WriteString(": Events that may be relevant:\n")
				writeEventList(bf, fes)
			}
			return errcat.User.New(bf.String())
		}
	}
}

func writeEventList(bf *strings.Builder, es []*events.Event) {
	now := time.Now()
	age := func(e *events.Event) string {
		return now.Sub(e.CreationTimestamp.Time).Truncate(time.Second).String()
	}
	object := func(e *events.Event) string {
		or := e.Regarding
		return strings.ToLower(or.Kind) + "/" + or.Name
	}
	ageLen, typeLen, reasonLen, objectLen := len("AGE"), len("TYPE"), len("REASON"), len("OBJECT")
	for _, e := range es {
		if l := len(age(e)); l > ageLen {
			ageLen = l
		}
		if l := len(e.Type); l > typeLen {
			typeLen = l
		}
		if l := len(e.Reason); l > reasonLen {
			reasonLen = l
		}
		if l := len(object(e)); l > objectLen {
			objectLen = l
		}
	}
	ageLen += 3
	typeLen += 3
	reasonLen += 3
	objectLen += 3
	fmt.Fprintf(bf, "%-*s%-*s%-*s%-*s%s\n", ageLen, "AGE", typeLen, "TYPE", reasonLen, "REASON", objectLen, "OBJECT", "MESSAGE")
	for _, e := range es {
		fmt.Fprintf(bf, "%-*s%-*s%-*s%-*s%s\n", ageLen, age(e), typeLen, e.Type, reasonLen, e.Reason, objectLen, object(e), e.Note)
	}
}

func unmarshalConfigMapEntry(y string, name, namespace string) (agentconfig.SidecarExt, error) {
	scx, err := agentconfig.UnmarshalYAML([]byte(y))
	if err != nil {
		return nil, fmt.Errorf("failed to parse entry for %s in ConfigMap %s.%s: %w", name, agentconfig.ConfigMap, namespace, err)
	}
	return scx, nil
}

// findIntercept finds the intercept configuration that matches the given InterceptSpec's service/service port.
func findIntercept(ac *agentconfig.Sidecar, spec *managerrpc.InterceptSpec) (foundCN *agentconfig.Container, foundIC *agentconfig.Intercept, err error) {
	spi := agentconfig.PortIdentifier(spec.ServicePortIdentifier)
	for _, cn := range ac.Containers {
		for _, ic := range cn.Intercepts {
			if !(spec.ServiceName == "" || spec.ServiceName == ic.ServiceName) {
				continue
			}
			if !(spi == "" || agentconfig.IsInterceptFor(spi, ic)) {
				continue
			}
			if foundIC == nil {
				foundCN = cn
				foundIC = ic
				continue
			}
			var msg string
			switch {
			case spec.ServiceName == "" && spi == "":
				msg = fmt.Sprintf("%s %s.%s has multiple interceptable service ports.\n"+
					"Please specify the service and/or service port you want to intercept "+
					"by passing the --service=<svc> and/or --port=<local:svcPortName> flag.",
					ac.WorkloadKind, ac.WorkloadName, ac.Namespace)
			case spec.ServiceName == "":
				msg = fmt.Sprintf("%s %s.%s has multiple interceptable services with port %s.\n"+
					"Please specify the service you want to intercept by passing the --service=<svc> flag.",
					ac.WorkloadKind, ac.WorkloadName, ac.Namespace, spi)
			case spi == "":
				msg = fmt.Sprintf("%s %s.%s has multiple interceptable ports in service %s.\n"+
					"Please specify the port you want to intercept by passing the --port=<local:svcPortName> flag.",
					ac.WorkloadKind, ac.WorkloadName, ac.Namespace, spec.ServiceName)
			default:
				msg = fmt.Sprintf("%s %s.%s intercept config is broken. Service %s, port %s is declared more than once\n",
					ac.WorkloadKind, ac.WorkloadName, ac.Namespace, spec.ServiceName, spi)
			}
			return nil, nil, errcat.User.New(msg)
		}
	}
	if foundIC != nil {
		return foundCN, foundIC, nil
	}

	ss := ""
	if spec.ServiceName != "" {
		if spi != "" {
			ss = fmt.Sprintf(" matching service %s, port %s", spec.ServiceName, spi)
		} else {
			ss = fmt.Sprintf(" matching service %s", spec.ServiceName)
		}
	} else if spi != "" {
		ss = fmt.Sprintf(" matching port %s", spi)
	}
	return nil, nil, errcat.User.Newf("%s %s.%s has no interceptable port%s", ac.WorkloadKind, ac.WorkloadName, ac.Namespace, ss)
}

type InterceptFinalizer func(ctx context.Context, interceptInfo *managerrpc.InterceptInfo) error

type interceptState struct {
	sync.Mutex
	lastInfoCh  chan *managerrpc.InterceptInfo
	finalizers  []InterceptFinalizer
	interceptID string
}

func newInterceptState(interceptID string) *interceptState {
	is := &interceptState{
		lastInfoCh:  make(chan *managerrpc.InterceptInfo),
		interceptID: interceptID,
	}
	return is
}

func (is *interceptState) addFinalizer(finalizer InterceptFinalizer) {
	is.Lock()
	defer is.Unlock()
	is.finalizers = append(is.finalizers, finalizer)
}

func (is *interceptState) terminate(ctx context.Context, interceptInfo *managerrpc.InterceptInfo) {
	is.Lock()
	defer is.Unlock()
	for i := len(is.finalizers) - 1; i >= 0; i-- {
		f := is.finalizers[i]
		if err := f(ctx, interceptInfo); err != nil {
			dlog.Errorf(ctx, "finalizer for intercept %s failed: %v", interceptInfo.Id, err)
		}
	}
}
