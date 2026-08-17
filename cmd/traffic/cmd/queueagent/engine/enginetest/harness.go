package enginetest

import (
	"context"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
	"github.com/telepresenceio/telepresence/v2/pkg/queuestate"
)

// Message is a source or shadow message, identified for the suite's
// dedup-by-ID tolerance of at-least-once delivery.
type Message struct {
	// ID is a unique identifier the scenarios use to recognize a message
	// across duplicates and across source/shadow boundaries. It is carried
	// in Headers by convention; a Probe implementation decides how.
	ID string

	// Headers is the message's header/property set, the field the engine
	// under test classifies routes against.
	Headers map[string]string

	// Body is the message payload. Scenarios do not inspect it beyond
	// round-tripping.
	Body []byte
}

// Probe drives and observes a real broker independently of the Engine under
// test. Each provider's test glue supplies an implementation wired to the
// same broker, source, and identity as the Harness's engines.
//
// ReadShadow's observer identity is independent of any identity the Engine
// or the application uses: reading through it never advances, and is never
// advanced by, the application's or a route's real consumer position. A
// scenario that wants to observe DrainRoute's or AppDrainShadow's proof of
// residue must not first read the same shadow through ReadShadow.
type Probe interface {
	// SeedSource publishes msgs to the real source stream.
	SeedSource(ctx context.Context, msgs []Message) error

	// SetAppConsumed makes the application's pre-split identity consume and
	// commit the first n messages published to the source, establishing the
	// position an activation's Prepare/Start must resume from.
	SetAppConsumed(ctx context.Context, n int) error

	// ReadShadow consumes up to expect messages from the named shadow
	// within timeout, using an observer identity distinct from the engine's
	// or the application's own. It returns fewer than expect messages,
	// never an error, when timeout elapses first.
	ReadShadow(ctx context.Context, shadow string, expect int, timeout time.Duration) ([]Message, error)

	// AppDrainShadow consumes the named app shadow to empty as the
	// redirected application identity would -- committing as it goes -- and
	// returns the number of messages consumed.
	AppDrainShadow(ctx context.Context, shadow string) (int, error)

	// ConsumeSourceAsApp consumes up to limit messages from the source,
	// within timeout, as the application's restored identity. It is used
	// after handback to prove the resume position.
	ConsumeSourceAsApp(ctx context.Context, limit int, timeout time.Duration) ([]Message, error)

	// ShadowExists reports whether the named shadow currently exists.
	ShadowExists(ctx context.Context, shadow string) (bool, error)

	// ShadowDepth returns a provider-defined reading of the named shadow
	// that strictly increases when a new message reaches it and never
	// increases otherwise. Observing it must not consume or alter anything;
	// scenarios use it to prove a pump stayed stopped.
	ShadowDepth(ctx context.Context, shadow string) (int64, error)
}

// Identity is the construction-time identity the Harness's engines share.
// RunConformance uses it only to compute the values an Engine under this
// identity must produce -- shadow and group names -- not to construct an
// Engine itself.
type Identity struct {
	InstallID    string
	WorkloadUID  string
	QueueName    string
	ActivationID string

	// SourceEnv is the application env var name Prepare's EnvOverrides must
	// point at the app shadow.
	SourceEnv string

	// GroupEnv is the application env var name Prepare's EnvOverrides must
	// point at the app group. Read only when HasGroups is true.
	GroupEnv string

	// HasGroups is true for providers with a consumer-group concept
	// (Kafka). RabbitMQ has none, and its Prepare contributes no group
	// override.
	HasGroups bool
}

// expectedOverrides returns the EnvOverrides an Engine constructed for id
// must return from Prepare, computed the same way the manager's validation
// computes it: from pkg/queuestate's naming functions, never from a value an
// Engine reports about itself.
func (id Identity) expectedOverrides() engine.EnvOverrides {
	overrides := engine.EnvOverrides{
		id.SourceEnv: queuestate.AppShadowName(id.InstallID, id.WorkloadUID, id.QueueName, id.ActivationID),
	}
	if id.HasGroups {
		overrides[id.GroupEnv] = queuestate.AppGroupName(id.InstallID, id.WorkloadUID, id.QueueName, id.ActivationID)
	}
	return overrides
}

// appShadow returns the bounded deterministic name of id's app shadow.
func (id Identity) appShadow() string {
	return queuestate.AppShadowName(id.InstallID, id.WorkloadUID, id.QueueName, id.ActivationID)
}

// sessionShadow returns the bounded deterministic name of routeID's session
// shadow under id.
func (id Identity) sessionShadow(routeID string) string {
	return queuestate.SessionShadowName(id.InstallID, id.WorkloadUID, id.QueueName, id.ActivationID, routeID)
}

// Harness wires the conformance suite to one provider's engine and broker.
type Harness struct {
	// NewEngine constructs a fresh engine.Engine for the identity and
	// activation carried in Identity. It is called once per instance a
	// scenario needs and may be called several times per scenario (for
	// example, Fencing constructs two); every instance it returns shares the
	// same identity, as a process restart would.
	NewEngine func(t *testing.T) engine.Engine

	// Probe drives and observes the real broker this Harness's engines are
	// wired to.
	Probe Probe

	// Identity is the construction-time identity NewEngine's instances
	// share.
	Identity Identity

	// Skip reports whether the suite must be skipped -- typically because no
	// broker is reachable from the test environment -- and, if so, why.
	Skip func() (reason string, skip bool)

	// BeginScenario, if non-nil, runs at the start of every scenario: the
	// point where a Probe resets any per-scenario state it privately tracks.
	BeginScenario func()
}
