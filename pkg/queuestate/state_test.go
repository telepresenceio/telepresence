package queuestate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/queueconfig"
)

const (
	testStateInstallID    = "install-abc"
	testStateWorkloadUID  = "uid-123"
	testStateActiveQueue  = "orders"
	testStateActiveActID  = "act-1"
	testStateRouteID      = "route-1"
	testStateDegradeQueue = "invoices"
	testStateDegradeActID = "act-2"
)

// validWorkloadState returns a fresh WorkloadState that satisfies every rule
// in [*WorkloadState.Validate]. Its BrokerResources are computed with this
// package's own naming functions, so it stays valid if those functions
// change. Callers mutate the returned document; each call allocates new
// slices and maps so mutations never alias between test cases.
func validWorkloadState() *WorkloadState {
	replicas := int32(3)
	expiry := metav1.NewTime(time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	return &WorkloadState{
		Generation:            7,
		InstallID:             testStateInstallID,
		WorkloadUID:           testStateWorkloadUID,
		WorkloadName:          "checkout",
		WorkloadKind:          WorkloadKindDeployment,
		Namespace:             "default",
		PreActivationReplicas: &replicas,
		OverrideGeneration:    4,
		Queues: []QueueState{
			{
				Name:         testStateActiveQueue,
				Phase:        Active,
				ActivationID: testStateActiveActID,
				Declaration: queueconfig.Queue{
					Name:      testStateActiveQueue,
					Container: "app",
					Provider: &queueconfig.Kafka{
						BrokersEnv:  "KAFKA_BROKERS",
						SourceEnv:   "ORDERS_TOPIC",
						GroupEnv:    "KAFKA_GROUP",
						OffsetReset: queueconfig.OffsetResetEarliest,
					},
				},
				Source: "orders-topic",
				Group:  "orders-group",
				ConnectionEnv: []core.EnvVar{
					{Name: "KAFKA_BROKERS", Value: "kafka:9092"},
				},
				EnvOverrides: map[string]string{
					"ORDERS_TOPIC": AppShadowName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue, testStateActiveActID),
					"KAFKA_GROUP":  AppGroupName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue, testStateActiveActID),
				},
				Routes: []Route{
					{
						ID:     testStateRouteID,
						Owner:  "session-owner-1",
						Filter: map[string]string{"x-telepresence-user": "thomas"},
						State:  RouteActive,
						Expiry: expiry,
					},
				},
				BrokerResources: []BrokerResource{
					{
						Kind: ResourceKindTopic,
						Name: AppShadowName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue, testStateActiveActID),
					},
					{
						Kind: ResourceKindGroup,
						Name: SplitterGroupName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue, testStateActiveActID),
					},
					{
						Kind: ResourceKindGroup,
						Name: AppGroupName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue, testStateActiveActID),
					},
					{
						Kind: ResourceKindTopic,
						Name: SessionShadowName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue,
							testStateActiveActID, testStateRouteID),
					},
					{
						Kind: ResourceKindGroup,
						Name: SessionGroupName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue,
							testStateActiveActID, testStateRouteID),
					},
				},
				Handoff: []byte{0x01, 0x02, 0x03},
			},
			{
				Name:  testStateDegradeQueue,
				Phase: Degraded,
				// ResumePhase Preparing is outside brokerInventoryPhases, so this queue needs no
				// recorded broker inventory despite being Degraded.
				ResumePhase:   Preparing,
				BlockedReason: "broker-unreachable",
				ActivationID:  testStateDegradeActID,
				Declaration: queueconfig.Queue{
					Name:      testStateDegradeQueue,
					Container: "app",
					Provider: &queueconfig.RabbitMQ{
						URLEnv:           "AMQP_URL",
						ManagementURLEnv: "RABBITMQ_MANAGEMENT_URL",
						SourceEnv:        "INVOICE_QUEUE",
					},
				},
				Source: "invoice-queue",
				ConnectionEnv: []core.EnvVar{
					{Name: "AMQP_URL", Value: "amqps://broker"},
					{Name: "RABBITMQ_MANAGEMENT_URL", Value: "https://broker:15672"},
				},
			},
		},
	}
}

// dropBrokerResource returns list without the entry named name, so a test can remove exactly
// one required resource while leaving the rest of the fixture's inventory intact.
func dropBrokerResource(list []BrokerResource, name string) []BrokerResource {
	out := make([]BrokerResource, 0, len(list))
	for _, br := range list {
		if br.Name != name {
			out = append(out, br)
		}
	}
	return out
}

func TestWorkloadStateRoundTrip(t *testing.T) {
	want := validWorkloadState()
	data, err := want.Marshal()
	require.NoError(t, err)

	got, err := UnmarshalYAML(data)
	require.NoError(t, err)

	// metav1.Time.UnmarshalJSON normalizes to the local time.Location (a
	// documented apimachinery quirk: Marshal always emits UTC, Unmarshal
	// always returns Local), so compare the represented instant rather than
	// the struct's internal time.Location, then normalize before the
	// structural comparison below.
	require.Len(t, got.Queues, len(want.Queues))
	for i := range want.Queues {
		wantRoutes, gotRoutes := want.Queues[i].Routes, got.Queues[i].Routes
		require.Len(t, gotRoutes, len(wantRoutes))
		for j := range wantRoutes {
			assert.True(t, wantRoutes[j].Expiry.Time.Equal(gotRoutes[j].Expiry.Time))
			gotRoutes[j].Expiry = wantRoutes[j].Expiry
		}
	}
	assert.Equal(t, want, got)

	// Re-marshaling the unmarshaled document reproduces the same bytes,
	// proving the round trip is lossless at the persisted-YAML level.
	data2, err := got.Marshal()
	require.NoError(t, err)
	assert.Equal(t, string(data), string(data2))
}

func TestWorkloadStateUnmarshalRejectsUnknownField(t *testing.T) {
	_, err := UnmarshalYAML([]byte("installID: install-abc\nbogusField: true\n"))
	assert.Error(t, err)
}

func TestWorkloadStateUnmarshalInvalidYAML(t *testing.T) {
	_, err := UnmarshalYAML([]byte("not: [valid"))
	assert.Error(t, err)
}

func TestWorkloadStateUnmarshalRejectsSemanticallyInvalidDocument(t *testing.T) {
	// A structurally well-formed but semantically empty document decodes
	// cleanly but carries none of the identity Validate requires.
	data, err := (&WorkloadState{}).Marshal()
	require.NoError(t, err)

	_, err = UnmarshalYAML(data)
	assert.Error(t, err)
}

func TestWorkloadStateValidateAcceptsFixture(t *testing.T) {
	assert.NoError(t, validWorkloadState().Validate())
}

func TestWorkloadStateValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *WorkloadState)
	}{
		{"empty installID", func(s *WorkloadState) { s.InstallID = "" }},
		{"empty workloadUID", func(s *WorkloadState) { s.WorkloadUID = "" }},
		{"invalid workloadName", func(s *WorkloadState) { s.WorkloadName = "Not Valid!" }},
		{"invalid namespace", func(s *WorkloadState) { s.Namespace = "Not_Valid" }},
		{"unsupported workloadKind", func(s *WorkloadState) { s.WorkloadKind = WorkloadKind("Job") }},
		{"negative generation", func(s *WorkloadState) { s.Generation = -1 }},
		{"negative overrideGeneration", func(s *WorkloadState) { s.OverrideGeneration = -1 }},
		{"negative preActivationReplicas", func(s *WorkloadState) {
			r := int32(-1)
			s.PreActivationReplicas = &r
		}},
		{"duplicate queue name", func(s *WorkloadState) { s.Queues[1].Name = s.Queues[0].Name }},
		{"invalid queue name", func(s *WorkloadState) { s.Queues[0].Name = "Not Valid!" }},
		{"undefined phase", func(s *WorkloadState) { s.Queues[0].Phase = Phase("Bogus") }},
		{"missing activationID", func(s *WorkloadState) { s.Queues[0].ActivationID = "" }},
		{"missing declaration provider", func(s *WorkloadState) { s.Queues[0].Declaration.Provider = nil }},
		{"missing source", func(s *WorkloadState) { s.Queues[0].Source = "" }},
		{"degraded without resumePhase", func(s *WorkloadState) { s.Queues[1].ResumePhase = "" }},
		{"degraded resumePhase is Degraded", func(s *WorkloadState) { s.Queues[1].ResumePhase = Degraded }},
		{"degraded resumePhase is Inactive", func(s *WorkloadState) { s.Queues[1].ResumePhase = Inactive }},
		{"degraded without blockedReason", func(s *WorkloadState) { s.Queues[1].BlockedReason = "" }},
		{"non-degraded with resumePhase set", func(s *WorkloadState) { s.Queues[0].ResumePhase = Preparing }},
		{"non-degraded with blockedReason set", func(s *WorkloadState) { s.Queues[0].BlockedReason = "x" }},
		{"empty route id", func(s *WorkloadState) { s.Queues[0].Routes[0].ID = "" }},
		{"duplicate route id", func(s *WorkloadState) {
			s.Queues[0].Routes = append(s.Queues[0].Routes, s.Queues[0].Routes[0])
		}},
		{"invalid route state", func(s *WorkloadState) { s.Queues[0].Routes[0].State = RouteState("Bogus") }},
		{"unknown broker resource kind", func(s *WorkloadState) {
			s.Queues[0].BrokerResources[0].Kind = ResourceKind("bogus")
		}},
		{"non-derivable broker resource name", func(s *WorkloadState) {
			s.Queues[0].BrokerResources[0].Name = "tp-app-not-derived-from-identity"
		}},
		{"duplicate broker resource name", func(s *WorkloadState) {
			s.Queues[0].BrokerResources = append(s.Queues[0].BrokerResources, s.Queues[0].BrokerResources[0])
		}},
		{"declaration fails schema validation", func(s *WorkloadState) {
			s.Queues[0].Declaration.Provider.(*queueconfig.Kafka).OffsetReset = "sideways"
		}},
		{"declaration name does not match queue name", func(s *WorkloadState) { s.Queues[0].Declaration.Name = "other" }},
		{"declaration container is implicit", func(s *WorkloadState) { s.Queues[0].Declaration.Container = "" }},
		{"kafka queue without group", func(s *WorkloadState) { s.Queues[0].Group = "" }},
		{"rabbitmq queue with group", func(s *WorkloadState) { s.Queues[1].Group = "invoices-group" }},
		{"connectionEnv missing a declared name", func(s *WorkloadState) { s.Queues[0].ConnectionEnv = nil }},
		{"connectionEnv carries an extra name", func(s *WorkloadState) {
			s.Queues[0].ConnectionEnv = append(s.Queues[0].ConnectionEnv, core.EnvVar{Name: "EXTRA", Value: "x"})
		}},
		{"connectionEnv bad source", func(s *WorkloadState) {
			s.Queues[0].ConnectionEnv[0].ValueFrom = &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{FieldPath: "status.podIP"},
			}
		}},
		{"envOverrides undeclared key", func(s *WorkloadState) { s.Queues[0].EnvOverrides["BOGUS"] = "x" }},
		{"envOverrides empty value", func(s *WorkloadState) { s.Queues[0].EnvOverrides["ORDERS_TOPIC"] = "" }},
		{"active queue with empty envOverrides", func(s *WorkloadState) { s.Queues[0].EnvOverrides = map[string]string{} }},
		{"envOverrides source value points at an arbitrary topic", func(s *WorkloadState) {
			s.Queues[0].EnvOverrides["ORDERS_TOPIC"] = "some-other-topic"
		}},
		{"kafka envOverrides missing the group key", func(s *WorkloadState) {
			delete(s.Queues[0].EnvOverrides, "KAFKA_GROUP")
		}},
		{"rabbitmq envOverrides containing a group key", func(s *WorkloadState) {
			s.Queues[1].EnvOverrides = map[string]string{
				"INVOICE_QUEUE": AppShadowName(testStateInstallID, testStateWorkloadUID, testStateDegradeQueue, testStateDegradeActID),
				"SOME_GROUP":    "bogus-group",
			}
		}},
		{"declaration collision across queues in the same container", func(s *WorkloadState) {
			s.Queues[1].Declaration.Provider.(*queueconfig.RabbitMQ).SourceEnv = "ORDERS_TOPIC"
		}},
		{"nil preActivationReplicas while a queue is in QuiescingApp", func(s *WorkloadState) {
			s.PreActivationReplicas = nil
			s.Queues[0].Phase = QuiescingApp
		}},
		{"nil preActivationReplicas while degraded resuming RestoringApp", func(s *WorkloadState) {
			s.PreActivationReplicas = nil
			s.Queues[1].ResumePhase = RestoringApp
		}},
		{"route without owner", func(s *WorkloadState) { s.Queues[0].Routes[0].Owner = "" }},
		{"route without expiry", func(s *WorkloadState) { s.Queues[0].Routes[0].Expiry = metav1.Time{} }},
		{"two overlapping active routes", func(s *WorkloadState) {
			r := s.Queues[0].Routes[0]
			r.ID = "route-2"
			s.Queues[0].Routes = append(s.Queues[0].Routes, r)
		}},
		{"topic broker resource relabelled as group kind", func(s *WorkloadState) {
			s.Queues[0].BrokerResources[0].Kind = ResourceKindGroup
		}},
		{"rabbitmq state carries a kafka-style group resource", func(s *WorkloadState) {
			s.Queues[1].BrokerResources = append(s.Queues[1].BrokerResources, BrokerResource{
				Kind: ResourceKindGroup,
				Name: SplitterGroupName(s.InstallID, s.WorkloadUID, s.Queues[1].Name, s.Queues[1].ActivationID),
			})
		}},
		{"kafka state carries a lock-queue name", func(s *WorkloadState) {
			s.Queues[0].BrokerResources = append(s.Queues[0].BrokerResources, BrokerResource{
				Kind: ResourceKindQueue,
				Name: LockQueueName(s.InstallID, s.WorkloadUID, s.Queues[0].Name, s.Queues[0].ActivationID),
			})
		}},
		{"inactive phase with lingering routes and resources", func(s *WorkloadState) { s.Queues[0].Phase = Inactive }},
		{"active kafka queue missing app shadow topic", func(s *WorkloadState) {
			s.Queues[0].BrokerResources = dropBrokerResource(s.Queues[0].BrokerResources,
				AppShadowName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue, testStateActiveActID))
		}},
		{"active kafka queue missing app group", func(s *WorkloadState) {
			s.Queues[0].BrokerResources = dropBrokerResource(s.Queues[0].BrokerResources,
				AppGroupName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue, testStateActiveActID))
		}},
		{"active kafka queue missing splitter group", func(s *WorkloadState) {
			s.Queues[0].BrokerResources = dropBrokerResource(s.Queues[0].BrokerResources,
				SplitterGroupName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue, testStateActiveActID))
		}},
		{"active kafka queue missing active route's session shadow", func(s *WorkloadState) {
			s.Queues[0].BrokerResources = dropBrokerResource(s.Queues[0].BrokerResources,
				SessionShadowName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue,
					testStateActiveActID, testStateRouteID))
		}},
		{"active kafka queue missing active route's session group", func(s *WorkloadState) {
			s.Queues[0].BrokerResources = dropBrokerResource(s.Queues[0].BrokerResources,
				SessionGroupName(testStateInstallID, testStateWorkloadUID, testStateActiveQueue,
					testStateActiveActID, testStateRouteID))
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validWorkloadState()
			c.mutate(s)
			assert.Error(t, s.Validate())
		})
	}
}

func TestWorkloadStateValidateAcceptsOverlappingRoutesWhenOneIsDraining(t *testing.T) {
	s := validWorkloadState()
	q := &s.Queues[0]
	draining := q.Routes[0]
	draining.ID = "route-2"
	draining.State = RouteDraining
	q.Routes = append(q.Routes, draining)
	assert.NoError(t, s.Validate())
}

func TestWorkloadStateValidateAcceptsDrainingRouteWithoutSessionResources(t *testing.T) {
	s := validWorkloadState()
	q := &s.Queues[0]
	draining := q.Routes[0]
	draining.ID = "route-2"
	draining.State = RouteDraining
	q.Routes = append(q.Routes, draining)
	// route-2 has no recorded session shadow or session group: draining routes' resources
	// stay permitted but are never required.
	assert.NoError(t, s.Validate())
}

func TestWorkloadStateValidateAcceptsPreparingQueueWithNoBrokerResources(t *testing.T) {
	s := validWorkloadState()
	q := &s.Queues[0]
	q.Phase = Preparing
	q.EnvOverrides = nil
	q.BrokerResources = nil
	// Preparing is outside both shadowConsumptionPhases and brokerInventoryPhases, so
	// neither envOverrides nor a broker inventory is required yet.
	assert.NoError(t, s.Validate())
}

func TestWorkloadStateValidateRequiresInventoryInRestoringApp(t *testing.T) {
	s := validWorkloadState()
	q := &s.Queues[0]
	q.Phase = RestoringApp
	// The application no longer consumes shadows in RestoringApp, so no overrides are
	// required, but every broker resource persists until CleaningUp reclaims it.
	q.EnvOverrides = nil
	q.Routes = nil
	q.BrokerResources = nil
	err := s.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "app shadow topic is required but not recorded")
	assert.ErrorContains(t, err, "splitter group is required but not recorded")
}

func TestWorkloadStateValidateRequiresInventoryWhileDegradedResumingRestoringApp(t *testing.T) {
	s := validWorkloadState()
	q := &s.Queues[0]
	q.Phase = Degraded
	q.ResumePhase = RestoringApp
	q.BlockedReason = "broker-unreachable"
	q.EnvOverrides = nil
	q.Routes = nil
	q.BrokerResources = nil
	err := s.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "app group is required but not recorded")
}

func TestWorkloadStateValidateRequiresInventoryInQuiescingApp(t *testing.T) {
	s := validWorkloadState()
	q := &s.Queues[0]
	q.Phase = QuiescingApp
	// Preparing completed durable resource creation before QuiescingApp was entered.
	q.EnvOverrides = nil
	q.Routes = nil
	q.BrokerResources = nil
	err := s.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "app shadow topic is required but not recorded")
}

func TestWorkloadStateValidateRejectsRabbitMQMissingLockQueueInInventoryPhase(t *testing.T) {
	s := validWorkloadState()
	q := &s.Queues[1]
	q.ResumePhase = Active
	q.EnvOverrides = map[string]string{
		"INVOICE_QUEUE": AppShadowName(s.InstallID, s.WorkloadUID, q.Name, q.ActivationID),
	}
	q.BrokerResources = []BrokerResource{
		{Kind: ResourceKindQueue, Name: AppShadowName(s.InstallID, s.WorkloadUID, q.Name, q.ActivationID)},
	}
	err := s.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "lock queue is required but not recorded")
}

func TestWorkloadStateValidateAcceptsSessionResourceNameForRecordedRoute(t *testing.T) {
	s := validWorkloadState()
	q := &s.Queues[0]
	// DrainGroupName is allowed for any recorded route regardless of state, and is not part
	// of the required inventory, unlike the session shadow/group the fixture already carries.
	q.BrokerResources = append(q.BrokerResources, BrokerResource{
		Kind: ResourceKindGroup,
		Name: DrainGroupName(s.InstallID, s.WorkloadUID, q.Name, q.ActivationID, q.Routes[0].ID),
	})
	assert.NoError(t, s.Validate())
}

func TestWorkloadStateValidateAcceptsCollidingNamesInDifferentContainers(t *testing.T) {
	s := validWorkloadState()
	// Both declarations name "ORDERS_TOPIC" as their sourceEnv, but resolve to different
	// containers, so no collision applies.
	s.Queues[1].Declaration.Container = "other-app"
	s.Queues[1].Declaration.Provider.(*queueconfig.RabbitMQ).SourceEnv = "ORDERS_TOPIC"
	assert.NoError(t, s.Validate())
}

func TestWorkloadStateValidateAcceptsNilReplicaSnapshotInSteadyState(t *testing.T) {
	s := validWorkloadState()
	s.PreActivationReplicas = nil
	// Neither queue is mid-cutover: one is Active, and Inactive carries none of the
	// artifacts an active split protects.
	s.Queues[1] = QueueState{Name: testStateDegradeQueue, Phase: Inactive}
	assert.NoError(t, s.Validate())
}

func TestValidateEnvVarValueAndValueFromMutuallyExclusive(t *testing.T) {
	e := &core.EnvVar{
		Name:  "FOO",
		Value: "bar",
		ValueFrom: &core.EnvVarSource{
			SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: "s"}, Key: "k"},
		},
	}
	err := validateEnvVar(e)
	require.Error(t, err)
	assert.ErrorContains(t, err, "mutually exclusive")
}

func TestValidateEnvVarTwoSelectorsRejected(t *testing.T) {
	e := &core.EnvVar{
		Name: "FOO",
		ValueFrom: &core.EnvVarSource{
			SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: "s"}, Key: "k"},
			FieldRef:     &core.ObjectFieldSelector{FieldPath: "status.podIP"},
		},
	}
	err := validateEnvVar(e)
	require.Error(t, err)
	assert.ErrorContains(t, err, "exactly one selector")
}

func TestValidateEnvVarEmptySelectorNameRejected(t *testing.T) {
	e := &core.EnvVar{
		Name: "FOO",
		ValueFrom: &core.EnvVarSource{
			SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: ""}, Key: "k"},
		},
	}
	err := validateEnvVar(e)
	require.Error(t, err)
	assert.ErrorContains(t, err, "name")
}

func TestValidateEnvVarInvalidNameRejected(t *testing.T) {
	e := &core.EnvVar{Name: "not valid!", Value: "x"}
	err := validateEnvVar(e)
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid")
}

func TestValidateEnvVarEmptyKeyRejected(t *testing.T) {
	e := &core.EnvVar{
		Name: "FOO",
		ValueFrom: &core.EnvVarSource{
			ConfigMapKeyRef: &core.ConfigMapKeySelector{LocalObjectReference: core.LocalObjectReference{Name: "cm"}, Key: ""},
		},
	}
	err := validateEnvVar(e)
	require.Error(t, err)
	assert.ErrorContains(t, err, "key is required")
}

func TestValidateEnvVarValidSecretKeyRefAccepted(t *testing.T) {
	e := &core.EnvVar{
		Name: "FOO",
		ValueFrom: &core.EnvVarSource{
			SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: "s"}, Key: "k"},
		},
	}
	assert.NoError(t, validateEnvVar(e))
}
