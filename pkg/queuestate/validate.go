package queuestate

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/telepresenceio/telepresence/v2/pkg/queueconfig"
)

// Validate applies the semantic rules a persisted WorkloadState document must satisfy beyond
// strict decoding: identity fields are well-formed, queue and route names are unique, phases
// are defined, degraded bookkeeping is internally consistent, a non-Inactive queue's
// declaration and persisted env are consistent with each other, route invariants hold, every
// recorded broker resource name and kind is recomputable from validated identity rather than
// trusted verbatim, and an Inactive queue carries none of the artifacts an active split
// protects. It returns an error that accumulates every violation found, each naming the
// offending queue, route, or field.
func (s *WorkloadState) Validate() error {
	var errs []error
	if s.InstallID == "" {
		errs = append(errs, errors.New("installID is required"))
	}
	if s.WorkloadUID == "" {
		errs = append(errs, errors.New("workloadUID is required"))
	}
	if verrs := validation.IsDNS1123Subdomain(s.WorkloadName); len(verrs) > 0 {
		errs = append(errs, fmt.Errorf("workloadName %q is invalid: %s", s.WorkloadName, verrs[0]))
	}
	if verrs := validation.IsDNS1123Label(s.Namespace); len(verrs) > 0 {
		errs = append(errs, fmt.Errorf("namespace %q is invalid: %s", s.Namespace, verrs[0]))
	}
	switch s.WorkloadKind {
	case WorkloadKindDeployment, WorkloadKindStatefulSet:
	default:
		errs = append(errs, fmt.Errorf("workloadKind %q is not a supported workload kind", s.WorkloadKind))
	}
	if s.Generation < 0 {
		errs = append(errs, fmt.Errorf("generation %d must not be negative", s.Generation))
	}
	if s.OverrideGeneration < 0 {
		errs = append(errs, fmt.Errorf("overrideGeneration %d must not be negative", s.OverrideGeneration))
	}
	if s.PreActivationReplicas != nil && *s.PreActivationReplicas < 0 {
		errs = append(errs, fmt.Errorf("preActivationReplicas %d must not be negative", *s.PreActivationReplicas))
	}

	names := make(map[string]bool, len(s.Queues))
	var decls []*queueconfig.Queue
	for i := range s.Queues {
		q := &s.Queues[i]
		if err := q.validate(s.InstallID, s.WorkloadUID); err != nil {
			errs = append(errs, err)
		}
		if q.Name != "" {
			if names[q.Name] {
				errs = append(errs, fmt.Errorf("queue %q: name is not unique", q.Name))
			}
			names[q.Name] = true
		}
		if q.Phase != Inactive {
			decls = append(decls, &q.Declaration)
		}
		if s.PreActivationReplicas == nil && requiresReplicaSnapshot(q.Phase, q.ResumePhase) {
			errs = append(errs, fmt.Errorf("preActivationReplicas is required: queue %q is in phase %q", q.Name, q.Phase))
		}
	}
	if err := queueconfig.ValidateCollisions(decls); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// effectivePhase returns resumePhase when phase is Degraded -- the phase whose rules govern a
// queue that is currently paused resuming it -- and phase otherwise.
func effectivePhase(phase, resumePhase Phase) Phase {
	if phase == Degraded {
		return resumePhase
	}
	return phase
}

// cutoverPhases is the set of phases in which a workload-wide cutover is in flight and
// PreActivationReplicas must already be recorded, so it can be restored when the cutover ends.
//
//nolint:gochecknoglobals // immutable phase set, not mutable state
var cutoverPhases = map[Phase]bool{
	QuiescingApp:       true,
	StartingPump:       true,
	RedirectingApp:     true,
	AbortingActivation: true,
	QuiescingHandback:  true,
	HandingBack:        true,
	RestoringApp:       true,
}

// requiresReplicaSnapshot reports whether a queue found in phase (or, while Degraded, resuming
// resumePhase) requires PreActivationReplicas to be recorded.
func requiresReplicaSnapshot(phase, resumePhase Phase) bool {
	return cutoverPhases[effectivePhase(phase, resumePhase)]
}

// validate checks one queue entry's semantic rules. installID and workloadUID are the
// identity components its broker resource names must be recomputable from.
func (q *QueueState) validate(installID, workloadUID string) error {
	label := fmt.Sprintf("queue %q", q.Name)
	var errs []error

	if q.Name == "" {
		errs = append(errs, errors.New("queue: name is required"))
	} else if verrs := validation.IsDNS1123Label(q.Name); len(verrs) > 0 {
		errs = append(errs, fmt.Errorf("%s: name is invalid: %s", label, verrs[0]))
	}

	if !definedPhase(q.Phase) {
		errs = append(errs, fmt.Errorf("%s: phase %q is not a defined phase", label, q.Phase))
	}

	if q.Phase != Inactive {
		if q.ActivationID == "" {
			errs = append(errs, fmt.Errorf("%s: activationID is required in phase %q", label, q.Phase))
		}
		if q.Declaration.Provider == nil {
			errs = append(errs, fmt.Errorf("%s: declaration.provider is required in phase %q", label, q.Phase))
		}
		if q.Source == "" {
			errs = append(errs, fmt.Errorf("%s: source is required in phase %q", label, q.Phase))
		}
		errs = append(errs, q.validateDeclaration(installID, workloadUID, label)...)
	} else {
		errs = append(errs, q.validateInactive(label)...)
	}

	if q.Phase == Degraded {
		if !definedPhase(q.ResumePhase) || q.ResumePhase == Degraded || q.ResumePhase == Inactive {
			errs = append(errs, fmt.Errorf("%s: resumePhase %q must be a defined phase other than Degraded or Inactive",
				label, q.ResumePhase))
		}
		if q.BlockedReason == "" {
			errs = append(errs, fmt.Errorf("%s: blockedReason is required while degraded", label))
		}
	} else {
		if q.ResumePhase != "" {
			errs = append(errs, fmt.Errorf("%s: resumePhase must be empty outside phase Degraded", label))
		}
		if q.BlockedReason != "" {
			errs = append(errs, fmt.Errorf("%s: blockedReason must be empty outside phase Degraded", label))
		}
	}

	errs = append(errs, q.validateRoutes(label)...)

	if err := q.validateBrokerResources(installID, workloadUID); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// validateDeclaration applies the rules a non-Inactive queue's declaration and persisted env
// must satisfy: the declaration passes its own schema validation, names this queue, carries
// the resolved container the manager selected at activation (never the implicit empty form),
// the persisted Group is present or absent according to the declared provider, and
// ConnectionEnv/EnvOverrides are consistent with the declaration. installID and workloadUID
// are the identity components EnvOverrides' shadow destinations must be recomputable from.
func (q *QueueState) validateDeclaration(installID, workloadUID, label string) []error {
	var errs []error

	if err := q.Declaration.ValidateSchema(); err != nil {
		errs = append(errs, fmt.Errorf("%s: declaration: %w", label, err))
	}
	if q.Declaration.Name != q.Name {
		errs = append(errs, fmt.Errorf("%s: declaration.name %q does not match the queue name", label, q.Declaration.Name))
	}
	if q.Declaration.Container == "" {
		errs = append(errs, fmt.Errorf("%s: declaration.container is required in persisted state", label))
	}

	switch q.Declaration.Provider.(type) {
	case *queueconfig.Kafka:
		if q.Group == "" {
			errs = append(errs, fmt.Errorf("%s: group is required for a kafka queue", label))
		}
	case *queueconfig.RabbitMQ:
		if q.Group != "" {
			errs = append(errs, fmt.Errorf("%s: group %q must be empty for a rabbitmq queue", label, q.Group))
		}
	}

	errs = append(errs, q.validateConnectionEnv(label)...)
	errs = append(errs, q.validateEnvOverrides(installID, workloadUID, label)...)
	return errs
}

// validateConnectionEnv requires ConnectionEnv entry names to be unique, each entry to pass
// validateEnvVar, and the set of entry names to equal the declaration's ConnectionEnvNames().
func (q *QueueState) validateConnectionEnv(label string) []error {
	var errs []error
	seen := make(map[string]bool, len(q.ConnectionEnv))
	for i := range q.ConnectionEnv {
		e := &q.ConnectionEnv[i]
		if seen[e.Name] {
			errs = append(errs, fmt.Errorf("%s: connectionEnv %q is not unique", label, e.Name))
		}
		seen[e.Name] = true
		if err := validateEnvVar(e); err != nil {
			errs = append(errs, fmt.Errorf("%s: connectionEnv %q: %w", label, e.Name, err))
		}
	}

	want := make(map[string]bool)
	for _, n := range q.Declaration.ConnectionEnvNames() {
		want[n] = true
	}
	for n := range want {
		if !seen[n] {
			errs = append(errs, fmt.Errorf("%s: connectionEnv is missing declared entry %q", label, n))
		}
	}
	for _, n := range slices.Sorted(maps.Keys(seen)) {
		if !want[n] {
			errs = append(errs, fmt.Errorf("%s: connectionEnv %q is not declared for this queue", label, n))
		}
	}
	return errs
}

// validateEnvVar validates e the way Kubernetes would: Name must be a valid env var name, Value
// and ValueFrom are mutually exclusive, and a non-nil ValueFrom must set exactly one selector,
// which must be secretKeyRef or configMapKeyRef -- fieldRef and resourceFieldRef would resolve
// against the queue-agent rather than the app pod, and are rejected like any other source.
func validateEnvVar(e *core.EnvVar) error {
	var errs []error
	if verrs := validation.IsEnvVarName(e.Name); len(verrs) > 0 {
		errs = append(errs, fmt.Errorf("name %q is invalid: %s", e.Name, verrs[0]))
	}
	if e.Value != "" && e.ValueFrom != nil {
		errs = append(errs, errors.New("value and valueFrom are mutually exclusive"))
	}
	if e.ValueFrom != nil {
		errs = append(errs, validateEnvVarSource(e.ValueFrom)...)
	}
	return errors.Join(errs...)
}

// validateEnvVarSource requires vf to set exactly one selector, and that selector to be
// secretKeyRef or configMapKeyRef, naming a valid Secret/ConfigMap and key.
func validateEnvVarSource(vf *core.EnvVarSource) []error {
	selectors := 0
	for _, set := range []bool{vf.FieldRef != nil, vf.ResourceFieldRef != nil, vf.SecretKeyRef != nil, vf.ConfigMapKeyRef != nil} {
		if set {
			selectors++
		}
	}
	if selectors != 1 {
		return []error{fmt.Errorf("valueFrom must set exactly one selector, found %d", selectors)}
	}

	switch {
	case vf.SecretKeyRef != nil:
		return validateKeySelector(vf.SecretKeyRef.Name, vf.SecretKeyRef.Key)
	case vf.ConfigMapKeyRef != nil:
		return validateKeySelector(vf.ConfigMapKeyRef.Name, vf.ConfigMapKeyRef.Key)
	default:
		return []error{errors.New("valueFrom must be secretKeyRef or configMapKeyRef")}
	}
}

// validateKeySelector requires name to be a valid DNS-1123 subdomain and key to be a non-empty,
// valid ConfigMap/Secret data key.
func validateKeySelector(name, key string) []error {
	var errs []error
	if verrs := validation.IsDNS1123Subdomain(name); len(verrs) > 0 {
		errs = append(errs, fmt.Errorf("name %q is invalid: %s", name, verrs[0]))
	}
	if key == "" {
		errs = append(errs, errors.New("key is required"))
	} else if verrs := validation.IsConfigMapKey(key); len(verrs) > 0 {
		errs = append(errs, fmt.Errorf("key %q is invalid: %s", key, verrs[0]))
	}
	return errs
}

// shadowConsumptionPhases is the set of phases -- including Degraded resuming one of them --
// in which the application consumes shadow resources, so the env overrides must already be
// recorded.
//
//nolint:gochecknoglobals // immutable phase set, not mutable state
var shadowConsumptionPhases = map[Phase]bool{
	RedirectingApp:    true,
	Active:            true,
	DrainingApp:       true,
	QuiescingHandback: true,
	HandingBack:       true,
}

// brokerInventoryPhases is the set of phases -- including Degraded resuming one of them --
// that lie after Preparing completed durable resource creation and before CleaningUp starts
// deleting, so the base broker inventory must be fully recorded. RestoringApp still requires
// it: the application no longer consumes shadows there, but every shadow and group persists
// until CleaningUp reclaims it, and cleanup deletes only recorded resources.
//
//nolint:gochecknoglobals // immutable phase set, not mutable state
var brokerInventoryPhases = map[Phase]bool{
	QuiescingApp:      true,
	StartingPump:      true,
	RedirectingApp:    true,
	Active:            true,
	DrainingApp:       true,
	QuiescingHandback: true,
	HandingBack:       true,
	RestoringApp:      true,
}

// validateEnvOverrides requires EnvOverrides, when non-empty, to equal exactly the override map
// this queue's declared provider produces for installID/workloadUID/q.ActivationID: sourceEnv
// pointed at the app shadow, and, for Kafka, groupEnv pointed at the app group. EnvOverrides is
// required in shadowConsumptionPhases and may be empty otherwise.
func (q *QueueState) validateEnvOverrides(installID, workloadUID, label string) []error {
	var errs []error
	if len(q.EnvOverrides) == 0 {
		if shadowConsumptionPhases[effectivePhase(q.Phase, q.ResumePhase)] {
			errs = append(errs, fmt.Errorf("%s: envOverrides is required in phase %q", label, q.Phase))
		}
		return errs
	}

	want := q.wantEnvOverrides(installID, workloadUID)
	if !maps.Equal(q.EnvOverrides, want) {
		errs = append(errs, fmt.Errorf("%s: envOverrides must be exactly %v, got %v", label, want, q.EnvOverrides))
	}
	return errs
}

// wantEnvOverrides returns the override map this queue's declared provider must exactly
// contribute: sourceEnv pointed at this activation's app shadow and, for Kafka, groupEnv
// pointed at this activation's app group.
func (q *QueueState) wantEnvOverrides(installID, workloadUID string) map[string]string {
	switch p := q.Declaration.Provider.(type) {
	case *queueconfig.Kafka:
		return map[string]string{
			p.SourceEnv: AppShadowName(installID, workloadUID, q.Name, q.ActivationID),
			p.GroupEnv:  AppGroupName(installID, workloadUID, q.Name, q.ActivationID),
		}
	case *queueconfig.RabbitMQ:
		return map[string]string{
			p.SourceEnv: AppShadowName(installID, workloadUID, q.Name, q.ActivationID),
		}
	default:
		return nil
	}
}

// validateInactive requires a queue in the terminal phase to carry none of the artifacts an
// active or recovering split protects: routes, overrides, broker resources, and handoff must
// all be empty.
func (q *QueueState) validateInactive(label string) []error {
	var errs []error
	if len(q.Routes) != 0 {
		errs = append(errs, fmt.Errorf("%s: routes must be empty in phase %q", label, Inactive))
	}
	if len(q.EnvOverrides) != 0 {
		errs = append(errs, fmt.Errorf("%s: envOverrides must be empty in phase %q", label, Inactive))
	}
	if len(q.BrokerResources) != 0 {
		errs = append(errs, fmt.Errorf("%s: brokerResources must be empty in phase %q", label, Inactive))
	}
	if len(q.Handoff) != 0 {
		errs = append(errs, fmt.Errorf("%s: handoff must be empty in phase %q", label, Inactive))
	}
	return errs
}

// validateRoutes applies the per-route rules: ids are present and unique, states are defined,
// owners and expiries are set, and no two Active routes' filters may overlap -- a route that
// is Draining accepts no new messages, so it is exempt from the overlap rule.
func (q *QueueState) validateRoutes(label string) []error {
	var errs []error
	routeIDs := make(map[string]bool, len(q.Routes))
	for i := range q.Routes {
		r := &q.Routes[i]
		switch {
		case r.ID == "":
			errs = append(errs, fmt.Errorf("%s: route id is required", label))
		case routeIDs[r.ID]:
			errs = append(errs, fmt.Errorf("%s: route %q: id is not unique", label, r.ID))
		default:
			routeIDs[r.ID] = true
		}
		switch r.State {
		case RouteActive, RouteDraining:
		default:
			errs = append(errs, fmt.Errorf("%s: route %q: state %q is not a defined route state", label, r.ID, r.State))
		}
		if r.Owner == "" {
			errs = append(errs, fmt.Errorf("%s: route %q: owner is required", label, r.ID))
		}
		if r.Expiry.IsZero() {
			errs = append(errs, fmt.Errorf("%s: route %q: expiry is required", label, r.ID))
		}
	}

	for i := range q.Routes {
		a := &q.Routes[i]
		if a.State != RouteActive {
			continue
		}
		for j := i + 1; j < len(q.Routes); j++ {
			b := &q.Routes[j]
			if b.State != RouteActive || !Overlaps(a.Filter, b.Filter) {
				continue
			}
			errs = append(errs, fmt.Errorf("%s: route %q and route %q have overlapping filters", label, a.ID, b.ID))
		}
	}
	return errs
}

// validateBrokerResources requires every recorded resource's name and kind to match one this
// package's naming functions would compute for this queue's persisted provider, identity, and
// recorded routes -- the tamper defense that keeps cleanup from ever trusting a verbatim name
// or kind -- and, in brokerInventoryPhases, requires this queue's base broker inventory to
// actually be recorded, so cleanup can find and delete everything an active split created
// rather than leaking infinite-retention shadows and groups. It skips this check when Declaration.Provider
// is nil; a non-Inactive queue with no provider is already reported by validateDeclaration.
func (q *QueueState) validateBrokerResources(installID, workloadUID string) error {
	if q.Declaration.Provider == nil {
		return nil
	}
	label := fmt.Sprintf("queue %q", q.Name)
	allowed := brokerResourceInventory(installID, workloadUID, q)

	var errs []error
	seen := make(map[string]bool, len(q.BrokerResources))
	// recorded holds only entries that already passed the allowed-set check above: a name
	// recorded with the wrong kind is reported there, and the required-inventory check below
	// treats it as absent rather than reporting it a second time.
	recorded := make(map[string]ResourceKind, len(q.BrokerResources))
	for i := range q.BrokerResources {
		br := &q.BrokerResources[i]
		if kind, ok := allowed[br.Name]; !ok {
			errs = append(errs, fmt.Errorf("%s: broker resource %q is not derivable from this queue's identity",
				label, br.Name))
		} else if br.Kind != kind {
			errs = append(errs, fmt.Errorf("%s: broker resource %q has kind %q, want %q", label, br.Name, br.Kind, kind))
		} else {
			recorded[br.Name] = br.Kind
		}
		if seen[br.Name] {
			errs = append(errs, fmt.Errorf("%s: broker resource %q is not unique", label, br.Name))
		}
		seen[br.Name] = true
	}

	if brokerInventoryPhases[effectivePhase(q.Phase, q.ResumePhase)] {
		errs = append(errs, q.requiredBrokerResources(installID, workloadUID, label, recorded)...)
	}

	return errors.Join(errs...)
}

// requiredResource names one entry of a queue's base broker inventory: the role it plays,
// so a missing-resource error is legible, and the name and kind cleanup must find to reclaim it.
type requiredResource struct {
	role string
	name string
	kind ResourceKind
}

// requiredBrokerResources returns a missing-resource error for every entry of q's base broker
// inventory that recorded does not carry under the exact name and kind this package's naming
// functions compute. A Draining route's session resources are permitted but never required;
// only RouteActive routes contribute session entries.
func (q *QueueState) requiredBrokerResources(installID, workloadUID, label string, recorded map[string]ResourceKind) []error {
	var required []requiredResource
	switch q.Declaration.Provider.(type) {
	case *queueconfig.Kafka:
		required = append(required,
			requiredResource{"app shadow topic", AppShadowName(installID, workloadUID, q.Name, q.ActivationID), ResourceKindTopic},
			requiredResource{"app group", AppGroupName(installID, workloadUID, q.Name, q.ActivationID), ResourceKindGroup},
			requiredResource{"splitter group", SplitterGroupName(installID, workloadUID, q.Name, q.ActivationID), ResourceKindGroup},
		)
		for i := range q.Routes {
			r := &q.Routes[i]
			if r.State != RouteActive {
				continue
			}
			required = append(required,
				requiredResource{
					fmt.Sprintf("session shadow for route %q", r.ID),
					SessionShadowName(installID, workloadUID, q.Name, q.ActivationID, r.ID), ResourceKindTopic,
				},
				requiredResource{
					fmt.Sprintf("session group for route %q", r.ID),
					SessionGroupName(installID, workloadUID, q.Name, q.ActivationID, r.ID), ResourceKindGroup,
				},
			)
		}
	case *queueconfig.RabbitMQ:
		required = append(required,
			requiredResource{"app shadow queue", AppShadowName(installID, workloadUID, q.Name, q.ActivationID), ResourceKindQueue},
			requiredResource{"lock queue", LockQueueName(installID, workloadUID, q.Name, q.ActivationID), ResourceKindQueue},
		)
		for i := range q.Routes {
			r := &q.Routes[i]
			if r.State != RouteActive {
				continue
			}
			required = append(required, requiredResource{
				fmt.Sprintf("session shadow for route %q", r.ID),
				SessionShadowName(installID, workloadUID, q.Name, q.ActivationID, r.ID), ResourceKindQueue,
			})
		}
	}

	var errs []error
	for _, req := range required {
		if recorded[req.name] == req.kind {
			continue
		}
		errs = append(errs, fmt.Errorf("%s: %s is required but not recorded: %q", label, req.role, req.name))
	}
	return errs
}

// brokerResourceInventory returns the broker resource names and kinds q's persisted provider
// may record, derived from q's identity and recorded routes. Kafka fencing uses a group
// instance ID rather than a named broker resource, so Kafka has no lock queue; RabbitMQ has no
// consumer groups, so every RabbitMQ resource is a queue.
func brokerResourceInventory(installID, workloadUID string, q *QueueState) map[string]ResourceKind {
	allowed := make(map[string]ResourceKind)
	switch q.Declaration.Provider.(type) {
	case *queueconfig.Kafka:
		allowed[AppShadowName(installID, workloadUID, q.Name, q.ActivationID)] = ResourceKindTopic
		allowed[AppGroupName(installID, workloadUID, q.Name, q.ActivationID)] = ResourceKindGroup
		allowed[SplitterGroupName(installID, workloadUID, q.Name, q.ActivationID)] = ResourceKindGroup
		for i := range q.Routes {
			routeID := q.Routes[i].ID
			allowed[SessionShadowName(installID, workloadUID, q.Name, q.ActivationID, routeID)] = ResourceKindTopic
			allowed[SessionGroupName(installID, workloadUID, q.Name, q.ActivationID, routeID)] = ResourceKindGroup
			allowed[DrainGroupName(installID, workloadUID, q.Name, q.ActivationID, routeID)] = ResourceKindGroup
		}
	case *queueconfig.RabbitMQ:
		allowed[AppShadowName(installID, workloadUID, q.Name, q.ActivationID)] = ResourceKindQueue
		allowed[LockQueueName(installID, workloadUID, q.Name, q.ActivationID)] = ResourceKindQueue
		for i := range q.Routes {
			routeID := q.Routes[i].ID
			allowed[SessionShadowName(installID, workloadUID, q.Name, q.ActivationID, routeID)] = ResourceKindQueue
		}
	}
	return allowed
}
