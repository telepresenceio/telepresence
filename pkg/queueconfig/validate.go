package queueconfig

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

// resolvedQueue pairs a queue with the container it resolved to and the label used to
// identify it in error messages.
type resolvedQueue struct {
	queue *Queue
	label string
	cn    *core.Container
}

// Validate applies the pod-template-dependent rules from the annotation schema: container
// selection, literal-env requirements for sourceEnv and Kafka groupEnv, resolvable-env
// requirements for connection and credential env vars, per-container env-var collisions,
// and TLS Secret references. It assumes tpl passed Parse's schema validation, so it does
// not re-check provider selection or field presence at the schema level. It returns an
// error that accumulates every violation found.
func (c *Config) Validate(tpl *core.PodTemplateSpec) error {
	var errs []error

	rs := make([]resolvedQueue, 0, len(c.Queues))
	for i := range c.Queues {
		q := &c.Queues[i]
		label := queueLabel(i, q.Name)
		cn, err := selectContainer(tpl, q.Container)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
			continue
		}
		rs = append(rs, resolvedQueue{queue: q, label: label, cn: cn})
	}

	errs = append(errs, checkEnvCollisions(rs)...)

	for _, r := range rs {
		if err := r.queue.validate(tpl, r.cn, r.label); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (q *Queue) validate(tpl *core.PodTemplateSpec, cn *core.Container, label string) error {
	p := q.Provider
	if p == nil {
		// Parse's schema validation reports provider selection; there is no
		// single provider whose env references could be checked here.
		return nil
	}

	var errs []error
	for _, e := range p.literalEnvs() {
		if err := requireLiteralEnv(cn, e.name, e.field, label); err != nil {
			errs = append(errs, err)
		}
	}
	for _, e := range p.resolvableEnvs() {
		if err := requireResolvableEnv(cn, e.name, e.field, label); err != nil {
			errs = append(errs, err)
		}
	}
	if t := p.tlsConfig(); t != nil {
		field := p.blockField() + ".tls.caSecret"
		if err := requireReferencedSecret(tpl, cn, t.CASecret, t.CAKey, field, label); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// namedEnv pairs an envRef with the label of the queue that declared it, for collision
// error messages.
type namedEnv struct {
	ref   envRef
	label string
}

// collisionEntry pairs a queue's provider and error-message label, the two things the shared
// pairwise collision rules need, independent of how the queue's container was resolved.
type collisionEntry struct {
	provider Provider
	label    string
}

// checkEnvCollisions enforces, within each container that queues in rs resolved to: override
// env names (literalEnvs) must be unique among themselves, and must not equal any
// connection/credential env name (resolvableEnvs) in the same container. Connection/credential
// names may repeat freely. Queues are grouped by container name, since selectContainer
// guarantees every resolvedQueue's cn belongs to tpl.Spec.Containers.
func checkEnvCollisions(rs []resolvedQueue) []error {
	byContainer := make(map[string][]collisionEntry, len(rs))
	for _, r := range rs {
		byContainer[r.cn.Name] = append(byContainer[r.cn.Name], collisionEntry{provider: r.queue.Provider, label: r.label})
	}
	return checkAllContainerGroups(byContainer)
}

// ValidateCollisions applies checkEnvCollisions's rules across queues whose Container is
// already resolved, rather than a pod template -- e.g. the declarations of a workload's
// persisted, still-active queues. Every queue's Container must be nonempty: an unresolved
// container here is a caller error, not an annotation the workload author wrote. It returns an
// error that accumulates every violation found.
func ValidateCollisions(queues []*Queue) error {
	var errs []error
	byContainer := make(map[string][]collisionEntry, len(queues))
	for _, q := range queues {
		if q.Container == "" {
			errs = append(errs, fmt.Errorf("%s: container is required for collision validation", q.label()))
			continue
		}
		byContainer[q.Container] = append(byContainer[q.Container], collisionEntry{provider: q.Provider, label: q.label()})
	}
	errs = append(errs, checkAllContainerGroups(byContainer)...)
	return errors.Join(errs...)
}

// checkAllContainerGroups applies checkGroupEnvCollisions to every container's group of
// entries, in deterministic container-name order.
func checkAllContainerGroups(byContainer map[string][]collisionEntry) []error {
	var errs []error
	for _, cn := range slices.Sorted(maps.Keys(byContainer)) {
		errs = append(errs, checkGroupEnvCollisions(byContainer[cn], cn)...)
	}
	return errs
}

// checkGroupEnvCollisions applies the collision rules from checkEnvCollisions to the entries
// in group, all of which belong to the container named cn.
func checkGroupEnvCollisions(group []collisionEntry, cn string) []error {
	var overrides, resolvables []namedEnv
	for _, entry := range group {
		p := entry.provider
		if p == nil {
			continue
		}
		for _, e := range p.literalEnvs() {
			if e.name != "" {
				overrides = append(overrides, namedEnv{ref: e, label: entry.label})
			}
		}
		for _, e := range p.resolvableEnvs() {
			if e.name != "" {
				resolvables = append(resolvables, namedEnv{ref: e, label: entry.label})
			}
		}
	}

	var errs []error
	for i, a := range overrides {
		for _, b := range overrides[i+1:] {
			if a.ref.name == b.ref.name {
				errs = append(errs, envCollisionError(a, b, cn))
			}
		}
		for _, b := range resolvables {
			if a.ref.name == b.ref.name {
				errs = append(errs, envCollisionError(a, b, cn))
			}
		}
	}
	return errs
}

// envCollisionError reports that a's env var name collides with b's, in container cn.
func envCollisionError(a, b namedEnv, cn string) error {
	return fmt.Errorf("%s: %s %q collides with %s %q of %s in container %q",
		a.label, a.ref.field, a.ref.name, b.ref.field, b.ref.name, b.label, cn)
}

// selectContainer returns the app container that owns the env vars referenced by a queue. An
// explicit name must exist in the pod template. An empty name resolves to the workload's sole
// app container, excluding any container named agentconfig.ContainerName; anything other than
// exactly one candidate is ambiguous.
func selectContainer(tpl *core.PodTemplateSpec, name string) (*core.Container, error) {
	cns := tpl.Spec.Containers
	if name != "" {
		if name == agentconfig.ContainerName {
			return nil, fmt.Errorf("container %q is the traffic-agent, not an app container", name)
		}
		for i := range cns {
			if cns[i].Name == name {
				return &cns[i], nil
			}
		}
		return nil, fmt.Errorf("container %q not found in pod template", name)
	}

	var candidate *core.Container
	count := 0
	for i := range cns {
		if cns[i].Name == agentconfig.ContainerName {
			continue
		}
		candidate = &cns[i]
		count++
	}
	if count != 1 {
		return nil, fmt.Errorf("container is required: the pod template has %d app containers", count)
	}
	return candidate, nil
}

// requireLiteralEnv requires name to be a literal EnvVar (no valueFrom, non-empty Value) in
// cn.Env. field identifies the offending schema field in error messages.
func requireLiteralEnv(cn *core.Container, name, field, label string) error {
	for i := range cn.Env {
		e := &cn.Env[i]
		if e.Name != name {
			continue
		}
		if e.ValueFrom != nil {
			return fmt.Errorf("%s: %s %q must be a literal env value, not valueFrom", label, field, name)
		}
		if e.Value == "" {
			return fmt.Errorf("%s: %s %q must have a non-empty literal value", label, field, name)
		}
		return nil
	}
	return fmt.Errorf("%s: %s %q is not an env var of container %q", label, field, name, cn.Name)
}

// requireResolvableEnv requires name to be an explicit entry in cn.Env whose source is a
// literal Value, secretKeyRef, or configMapKeyRef. fieldRef, resourceFieldRef, and names
// supplied only through envFrom are rejected. field identifies the offending schema field in
// error messages.
func requireResolvableEnv(cn *core.Container, name, field, label string) error {
	for i := range cn.Env {
		e := &cn.Env[i]
		if e.Name != name {
			continue
		}
		vf := e.ValueFrom
		switch {
		case vf == nil:
			return nil
		case vf.SecretKeyRef != nil, vf.ConfigMapKeyRef != nil:
			return nil
		case vf.FieldRef != nil:
			return fmt.Errorf("%s: %s %q must not use fieldRef; it would resolve against the queue-agent, not the app pod",
				label, field, name)
		case vf.ResourceFieldRef != nil:
			return fmt.Errorf("%s: %s %q must not use resourceFieldRef; it would resolve against the queue-agent, "+
				"not the app pod", label, field, name)
		default:
			return fmt.Errorf("%s: %s %q has an unsupported env source", label, field, name)
		}
	}
	return fmt.Errorf("%s: %s %q is not an explicit env var of container %q; envFrom is not supported",
		label, field, name, cn.Name)
}

// requireReferencedSecret requires the Secret key named by secret/key to already be exposed
// to cn: either an env entry's valueFrom.secretKeyRef naming that exact Secret and key, or a
// pod volume with a plain Secret source for that Secret, mounted by cn without SubPath or
// SubPathExpr, whose Items either expose every key (Items is empty) or explicitly name key. A
// SubPath/SubPathExpr mount exposes a single file, not proof that key is available, so it does
// not count. Projected volumes are rejected: a workload author must expose the CA key directly,
// not through a projection. field identifies the offending schema field in error messages.
func requireReferencedSecret(tpl *core.PodTemplateSpec, cn *core.Container, secret, key, field, label string) error {
	for i := range cn.Env {
		vf := cn.Env[i].ValueFrom
		if vf != nil && vf.SecretKeyRef != nil && vf.SecretKeyRef.Name == secret && vf.SecretKeyRef.Key == key {
			return nil
		}
	}

	mounted := make(map[string]bool, len(cn.VolumeMounts))
	for _, m := range cn.VolumeMounts {
		if m.SubPath == "" && m.SubPathExpr == "" {
			mounted[m.Name] = true
		}
	}
	for _, v := range tpl.Spec.Volumes {
		if !mounted[v.Name] || v.Secret == nil || v.Secret.SecretName != secret {
			continue
		}
		if len(v.Secret.Items) == 0 {
			return nil
		}
		for _, item := range v.Secret.Items {
			if item.Key == key {
				return nil
			}
		}
	}
	return fmt.Errorf("%s: %s secret %q key %q is not exposed by container %q",
		label, field, secret, key, cn.Name)
}
