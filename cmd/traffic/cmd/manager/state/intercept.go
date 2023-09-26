package state

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
	events "k8s.io/api/events/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	managerrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
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
	replacePolicy agentconfig.ReplacePolicy,
) (pi *managerrpc.PreparedIntercept, err error) {
	ctx, cancel := context.WithTimeout(ctx, managerutil.GetEnv(ctx).AgentArrivalTimeout)
	defer cancel()
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
	wl, err := tracing.GetWorkload(ctx, spec.Agent, spec.Namespace, spec.WorkloadKind)
	if err != nil {
		if errors2.IsNotFound(err) {
			err = errcat.User.New(err)
		}
		return interceptError(err)
	}

	failedCreateCh, err := watchFailedInjectionEvents(ctx, spec.Agent, spec.Namespace)
	if err != nil {
		return interceptError(err)
	}

	sce, err := s.getOrCreateAgentConfig(ctx, wl, spec, replacePolicy)
	if err != nil {
		return interceptError(err)
	}
	ac := sce.AgentConfig()
	_, ic, err := findIntercept(ac, spec)
	if err != nil {
		return interceptError(err)
	}
	if err = s.waitForAgent(ctx, ac.AgentName, ac.Namespace, failedCreateCh); err != nil {
		return interceptError(err)
	}
	return &managerrpc.PreparedIntercept{
		Namespace:       spec.Namespace,
		ServiceUid:      string(ic.ServiceUID),
		ServiceName:     ic.ServiceName,
		ServicePortName: ic.ServicePortName,
		ServicePort:     int32(ic.ServicePort),
		AgentImage:      ac.AgentImage,
		WorkloadKind:    ac.WorkloadKind,
	}, nil
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

func (s *state) getOrCreateAgentConfig(
	ctx context.Context,
	wl k8sapi.Workload,
	spec *managerrpc.InterceptSpec,
	replacePolicy agentconfig.ReplacePolicy,
) (sc agentconfig.SidecarExt, err error) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "state.getOrCreateAgentConfig")
	defer tracing.EndAndRecord(span, err)

	ns := wl.GetNamespace()
	s.mu.Lock()
	cl, ok := s.cfgMapLocks[ns]
	if !ok {
		cl = &sync.Mutex{}
		s.cfgMapLocks[ns] = cl
	}
	s.mu.Unlock()

	cl.Lock()
	defer cl.Unlock()

	cmAPI := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(ns)
	cm, err := loadConfigMap(ctx, cmAPI, ns)
	if err != nil {
		return nil, err
	}
	return s.loadAgentConfig(ctx, cmAPI, cm, wl, spec, replacePolicy)
}

func loadConfigMap(ctx context.Context, cmAPI typed.ConfigMapInterface, namespace string) (cm *core.ConfigMap, err error) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "state.loadConfigMap")
	defer tracing.EndAndRecord(span, err)

	cm, err = cmAPI.Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
	if err == nil {
		span.SetAttributes(attribute.Bool("tel2.cm-found", true))
		return cm, nil
	}
	if !errors2.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get ConfigMap %s.%s: %w", agentconfig.ConfigMap, namespace, err)
	}
	span.SetAttributes(attribute.Bool("tel2.cm-found", false))
	cm, err = cmAPI.Create(ctx, &core.ConfigMap{
		TypeMeta: meta.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: meta.ObjectMeta{
			Name:      agentconfig.ConfigMap,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       agentconfig.ConfigMap,
				"app.kubernetes.io/created-by": "traffic-manager",
				"app.kubernetes.io/version":    strings.TrimPrefix(version.Version, "v"),
			},
		},
	}, meta.CreateOptions{})
	if err != nil {
		err = fmt.Errorf("failed to create ConfigMap %s.%s: %w", agentconfig.ConfigMap, namespace, err)
	}
	return cm, err
}

func (s *state) loadAgentConfig(
	ctx context.Context,
	cmAPI typed.ConfigMapInterface,
	cm *core.ConfigMap,
	wl k8sapi.Workload,
	spec *managerrpc.InterceptSpec,
	replacePolicy agentconfig.ReplacePolicy,
) (sc agentconfig.SidecarExt, err error) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "state.loadAgentConfig")
	defer tracing.EndAndRecord(span, err)

	extended := s.isExtended(spec)
	enabled, err := checkInterceptAnnotations(wl)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(
		attribute.Bool("tel2.enabled", enabled),
		attribute.Bool("tel2.extended", extended),
	)
	if !enabled {
		return nil, errcat.User.Newf("%s %s.%s is not interceptable", wl.GetKind(), wl.GetName(), wl.GetNamespace())
	}

	agentImage := managerutil.GetAgentImage(ctx)
	if err = s.self.ValidateAgentImage(agentImage, extended); err != nil {
		return nil, err
	}
	span.SetAttributes(
		attribute.String("tel2.agent-image", agentImage),
	)

	update := func(sce agentconfig.SidecarExt) (err error) {
		ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "state.loadAgentConfig.update", trace.WithAttributes(
			attribute.String("tel2.workload-name", wl.GetName()),
			attribute.String("tel2.cm-name", agentconfig.ConfigMap),
			attribute.String("tel2.cm-namespace", wl.GetNamespace()),
		))
		defer tracing.EndAndRecord(span, err)
		yml, err := sce.Marshal()
		if err != nil {
			return err
		}
		cm.Data[wl.GetName()] = string(yml)
		if _, err := cmAPI.Update(ctx, cm, meta.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed update entry for %s in ConfigMap %s.%s: %w", wl.GetName(), agentconfig.ConfigMap, wl.GetNamespace(), err)
		}
		return err
	}

	var sce agentconfig.SidecarExt
	if y, ok := cm.Data[wl.GetName()]; ok {
		span.AddEvent("workload-config-found")
		if sce, err = unmarshalConfigMapEntry(y, wl.GetName(), wl.GetNamespace()); err != nil {
			return nil, err
		}
		ac := sce.AgentConfig()
		if ac.Create {
			// This may happen if someone else is doing the initial intercept at the exact (well, more or less) same time
			if sce, err = waitForConfigMapUpdate(ctx, cmAPI, wl.GetName(), wl.GetNamespace()); err != nil {
				return nil, err
			}
			ac = sce.AgentConfig()
		}
		doUpdate := false
		// If the agentImage has changed, and the extended image is requested, then update
		if ac.AgentImage != agentImage && extended {
			span.AddEvent("agent-image-changed")
			ac.AgentImage = agentImage
			doUpdate = true
		}
		for _, cn := range ac.Containers {
			if cn.Replace != replacePolicy {
				span.AddEvent("container-replace-changed")
				if (cn.Replace == agentconfig.ReplacePolicyActive && replacePolicy == agentconfig.ReplacePolicyInactive) ||
					(cn.Replace == agentconfig.ReplacePolicyInactive && replacePolicy == agentconfig.ReplacePolicyActive) {
					span.AddEvent("container-replace-accepted")
					cn.Replace = replacePolicy
					doUpdate = true
				} else {
					span.AddEvent("container-replace-rejected")
					flagValue := cn.Replace != agentconfig.ReplacePolicyNever
					return nil, errcat.User.Newf("intercept %s.%s has the --replace flag"+
						" set to %t, please use 'telepresence uninstall --agent %s'"+
						" then 'telepresence intercept' again to change it",
						spec.Agent, spec.Namespace, flagValue, spec.Agent)
				}
			}
		}
		if doUpdate {
			if err = update(sce); err != nil {
				return nil, err
			}
		}
	} else {
		span.AddEvent("workload-config-not-found")
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		var gc agentmap.GeneratorConfig
		if gc, err = agentmap.GeneratorConfigFunc(agentImage); err != nil {
			return nil, err
		}
		if sce, err = gc.Generate(ctx, wl, replacePolicy, nil); err != nil {
			return nil, err
		}
		if err = update(sce); err != nil {
			return nil, err
		}
	}
	return sce, nil
}

func checkInterceptAnnotations(wl k8sapi.Workload) (bool, error) {
	pod := wl.GetPodTemplate()
	a := pod.Annotations
	if a == nil {
		return true, nil
	}

	webhookEnabled := true
	manuallyManaged := a[install.ManualInjectAnnotation] == "true"
	ia := a[install.InjectAnnotation]
	switch ia {
	case "":
		webhookEnabled = !manuallyManaged
	case "enabled":
	case "false", "disabled":
		webhookEnabled = false
	default:
		return false, errcat.User.Newf(
			"%s is not a valid value for the %s.%s/%s annotation",
			ia, wl.GetName(), wl.GetNamespace(), install.ManualInjectAnnotation)
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
			wl.GetName(), wl.GetNamespace(), install.ManualInjectAnnotation)
	}
	return true, nil
}

// Wait for the cluster's mutating webhook injector to do its magic. It will update the
// configMap once it's done.
func waitForConfigMapUpdate(ctx context.Context, cmAPI typed.ConfigMapInterface, agentName, namespace string) (agentconfig.SidecarExt, error) {
	wi, err := cmAPI.Watch(ctx, meta.SingleObject(meta.ObjectMeta{
		Name:      agentconfig.ConfigMap,
		Namespace: namespace,
	}))
	if err != nil {
		return nil, fmt.Errorf("watch of ConfigMap  %s failed: %w", agentconfig.ConfigMap, ctx.Err())
	}
	defer wi.Stop()

	for {
		select {
		case <-ctx.Done():
			v := "canceled"
			c := codes.Canceled
			if ctx.Err() == context.DeadlineExceeded {
				v = "timed out"
				c = codes.DeadlineExceeded
			}
			return nil, status.Error(c, fmt.Sprintf("watch of ConfigMap %s[%s]: request %s", agentconfig.ConfigMap, agentName, v))
		case ev, ok := <-wi.ResultChan():
			if !ok {
				return nil, status.Error(codes.Canceled, fmt.Sprintf("watch of ConfigMap  %s[%s]: channel closed", agentconfig.ConfigMap, agentName))
			}
			if !(ev.Type == watch.Added || ev.Type == watch.Modified) {
				continue
			}
			if m, ok := ev.Object.(*core.ConfigMap); ok {
				if y, ok := m.Data[agentName]; ok {
					scx, err := unmarshalConfigMapEntry(y, agentName, namespace)
					if err != nil {
						return nil, err
					}
					if !scx.AgentConfig().Create {
						return scx, nil
					}
				}
			}
		}
	}
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
					msg = fmt.Sprintf("%s\nThe logs of %s %s might provide more details", msg, fe.Regarding.Kind, fe.Regarding.Name)
				case "FailedCreate", "FailedScheduling":
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

func (is *interceptState) terminate(ctx context.Context, interceptInfo *managerrpc.InterceptInfo) error {
	is.Lock()
	defer is.Unlock()
	var err error
	for i := len(is.finalizers) - 1; i >= 0; i-- {
		f := is.finalizers[i]
		tErr := f(ctx, interceptInfo)
		if tErr != nil {
			err = multierror.Append(err, tErr)
		}
	}
	return err
}
