package managers

import (
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/labels"
)

const (
	// ManagerNamespace is the stable namespace the single rtest traffic-manager
	// release lives in.
	ManagerNamespace = "rtest-manager"

	// TestServiceAccount is the ServiceAccount that clientRbac and managerRbac
	// grant access to, and the identity connections use with --as.
	TestServiceAccount = "telepresence-test-developer"

	// ManagedNamespaceLabel is the label key the namespaceSelector matches.
	// Namespaces the manager should manage carry this label.
	ManagedNamespaceLabel = "rtest.telepresence.io/managed"
)

// Values is the typed subset of the telepresence-oss chart values used by the
// manager catalog. Field names and JSON tags mirror charts/telepresence-oss's
// values.schema.yaml.
type Values struct {
	LogLevel      string        `json:"logLevel,omitempty"`
	Image         Image         `json:"image,omitzero"`
	Agent         AgentValues   `json:"agent,omitzero"`
	AgentInjector AgentInjector `json:"agentInjector,omitzero"`
	ClientRbac    Rbac          `json:"clientRbac,omitzero"`
	ManagerRbac   ManagerRbac   `json:"managerRbac,omitzero"`
	Timeouts      Timeouts      `json:"timeouts,omitzero"`
	// Namespaces and NamespaceSelector are mutually exclusive per the chart
	// (values.schema.yaml's namespaces/namespaceSelector descriptions):
	// setting a static Namespaces list must null NamespaceSelector, and
	// Merge does so (see the Namespaces handling below).
	Namespaces        []string         `json:"namespaces,omitempty"`
	NamespaceSelector *labels.Selector `json:"namespaceSelector,omitempty"`
	// PodCIDRs is only meaningful when PodCIDRStrategy is "environment"
	// (values.schema.yaml's podCIDRs description).
	PodCIDRs        []string `json:"podCIDRs,omitempty"`
	PodCIDRStrategy string   `json:"podCIDRStrategy,omitempty"`
	// ExtraEnv/ExtraVolumes/ExtraVolumeMounts mirror the chart's own
	// extraEnv/extraVolumes/extraVolumeMounts passthrough values, applied to
	// the traffic-manager container/pod. cover.go appends to these in
	// coverage mode (RTEST_COVER=1) to mount the GOCOVERDIR hostPath.
	ExtraEnv          []corev1.EnvVar      `json:"extraEnv,omitempty"`
	ExtraVolumes      []corev1.Volume      `json:"extraVolumes,omitempty"`
	ExtraVolumeMounts []corev1.VolumeMount `json:"extraVolumeMounts,omitempty"`
	// Usage has no omit option: usage.enabled=false must always reach the
	// chart, whose own default is true.
	Usage Usage `json:"usage"`
}

// AgentInjector is the chart's agentInjector.* shape, restricted to the
// fields the catalog configures: whether the webhook is enabled at all, the
// injection policy, and the mutating webhook's certificate/reinvocation
// settings.
type AgentInjector struct {
	// Enabled is a pointer so InjectorDisabled() can force it to false: a
	// plain bool can't be distinguished from "not set" by Merge, which would
	// make agentInjector.enabled=false unreachable through the overlay.
	Enabled      *bool       `json:"enabled,omitempty"`
	InjectPolicy string      `json:"injectPolicy,omitempty"`
	Certificate  Certificate `json:"certificate,omitzero"`
	Webhook      Webhook     `json:"webhook,omitzero"`
}

// Certificate is the chart's agentInjector.certificate.* shape, restricted
// to the fields the catalog configures.
type Certificate struct {
	AccessMethod string `json:"accessMethod,omitempty"`
	Regenerate   bool   `json:"regenerate,omitempty"`
}

// Webhook is the chart's agentInjector.webhook.* shape, restricted to the
// field the catalog configures.
type Webhook struct {
	ReinvocationPolicy string `json:"reinvocationPolicy,omitempty"`
}

// Image is the chart's image.* and agent.image.* shape.
type Image struct {
	Registry   string `json:"registry,omitempty"`
	Name       string `json:"name,omitempty"`
	Tag        string `json:"tag,omitempty"`
	PullPolicy string `json:"pullPolicy,omitempty"`
}

// AgentValues is the chart's agent.* shape, restricted to the image settings
// the catalog configures.
type AgentValues struct {
	Image Image `json:"image,omitzero"`
}

// Rbac is the chart's clientRbac shape.
type Rbac struct {
	Create   bool             `json:"create"`
	Subjects []rbacv1.Subject `json:"subjects,omitempty"`
}

// ManagerRbac is the chart's managerRbac shape, which unlike clientRbac has
// no subjects.
type ManagerRbac struct {
	Create bool `json:"create"`
}

// Timeouts is the chart's timeouts.* shape.
type Timeouts struct {
	AgentArrival string `json:"agentArrival,omitempty"`
}

// Usage is the chart's usage.* shape, restricted to the enabled flag: no
// rtest spec ever points usage at a real collector.
type Usage struct {
	Enabled bool `json:"enabled"`
}

// Baseline returns the manager values every rtest spec starts from: debug
// logging, the given image coordinates for both the manager and the agent,
// usage reporting disabled, the agent-arrival timeout the framework's test
// waits are tuned for, a namespaceSelector matching ManagedNamespaceLabel=
// selectorLabel, and clientRbac/managerRbac granting TestServiceAccount
// access.
func Baseline(reg, tag, pullPolicy, selectorLabel string) Values {
	image := Image{Registry: reg, Tag: tag, PullPolicy: pullPolicy}
	subjects := []rbacv1.Subject{{
		Kind:      "ServiceAccount",
		Name:      TestServiceAccount,
		Namespace: ManagerNamespace,
	}}
	return Values{
		LogLevel: "debug",
		Image:    image,
		Agent:    AgentValues{Image: image},
		ClientRbac: Rbac{
			Create:   true,
			Subjects: subjects,
		},
		ManagerRbac: ManagerRbac{Create: true},
		Timeouts:    Timeouts{AgentArrival: "60s"},
		NamespaceSelector: &labels.Selector{
			MatchLabels: map[string]string{ManagedNamespaceLabel: selectorLabel},
		},
		Usage: Usage{Enabled: false},
	}
}

// Merge layers over on top of base: any field of over that is set (non-zero,
// non-nil, non-empty) replaces the corresponding field of base; unset fields
// keep base's value. It is used to apply a catalog spec's overlay on top of
// Baseline.
func Merge(base, over Values) Values {
	m := base
	if over.LogLevel != "" {
		m.LogLevel = over.LogLevel
	}
	m.Image = mergeImage(m.Image, over.Image)
	m.Agent.Image = mergeImage(m.Agent.Image, over.Agent.Image)
	m.AgentInjector = mergeAgentInjector(m.AgentInjector, over.AgentInjector)
	m.ClientRbac = mergeRbac(m.ClientRbac, over.ClientRbac)
	if over.ManagerRbac.Create {
		m.ManagerRbac.Create = true
	}
	if over.Timeouts.AgentArrival != "" {
		m.Timeouts.AgentArrival = over.Timeouts.AgentArrival
	}
	if over.NamespaceSelector != nil {
		m.NamespaceSelector = over.NamespaceSelector
	}
	if over.Namespaces != nil {
		// namespaces and namespaceSelector are mutually exclusive in the
		// chart: a static list always wins over (and nulls) the selector,
		// regardless of the order the two overlays are declared in.
		m.Namespaces = over.Namespaces
		m.NamespaceSelector = nil
	}
	if over.PodCIDRs != nil {
		m.PodCIDRs = over.PodCIDRs
	}
	if over.PodCIDRStrategy != "" {
		m.PodCIDRStrategy = over.PodCIDRStrategy
	}
	if over.ExtraEnv != nil {
		m.ExtraEnv = over.ExtraEnv
	}
	if over.ExtraVolumes != nil {
		m.ExtraVolumes = over.ExtraVolumes
	}
	if over.ExtraVolumeMounts != nil {
		m.ExtraVolumeMounts = over.ExtraVolumeMounts
	}
	if over.Usage.Enabled {
		m.Usage.Enabled = true
	}
	return m
}

func mergeImage(base, over Image) Image {
	if over.Registry != "" {
		base.Registry = over.Registry
	}
	if over.Name != "" {
		base.Name = over.Name
	}
	if over.Tag != "" {
		base.Tag = over.Tag
	}
	if over.PullPolicy != "" {
		base.PullPolicy = over.PullPolicy
	}
	return base
}

func mergeRbac(base, over Rbac) Rbac {
	if over.Create {
		base.Create = true
	}
	if over.Subjects != nil {
		base.Subjects = over.Subjects
	}
	return base
}

func mergeAgentInjector(base, over AgentInjector) AgentInjector {
	if over.Enabled != nil {
		base.Enabled = over.Enabled
	}
	if over.InjectPolicy != "" {
		base.InjectPolicy = over.InjectPolicy
	}
	base.Certificate = mergeCertificate(base.Certificate, over.Certificate)
	base.Webhook = mergeWebhook(base.Webhook, over.Webhook)
	return base
}

func mergeCertificate(base, over Certificate) Certificate {
	if over.AccessMethod != "" {
		base.AccessMethod = over.AccessMethod
	}
	if over.Regenerate {
		base.Regenerate = true
	}
	return base
}

func mergeWebhook(base, over Webhook) Webhook {
	if over.ReinvocationPolicy != "" {
		base.ReinvocationPolicy = over.ReinvocationPolicy
	}
	return base
}
