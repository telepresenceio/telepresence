package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine/enginetest"
)

const (
	// testIDHeaderKey carries a Message.ID across the wire. It is the
	// Probe's own bookkeeping, added on top of a message's real headers on
	// publish and stripped back off on read, so it never affects route
	// classification (which only ever looks at a filter's own keys).
	testIDHeaderKey = "x-tp-test-id"

	probeTimeout      = 15 * time.Second
	probeIdleTimeout  = 3 * time.Second
	probeDrainQos     = 16
	seedConfirmWindow = 10 * time.Second
	getPollInterval   = 200 * time.Millisecond
)

// TestConformance runs the shared engine.Engine conformance suite against a
// real RabbitMQ broker, reachable via TP_TEST_AMQP_URL (AMQP) and
// TP_TEST_RABBITMQ_MGMT_URL (management HTTP API), once for a classic source
// and once for a quorum source. The suite is skipped, not failed, when
// either env var is unset. The two variants run in parallel (see the
// t.Parallel() call in each subtest below) and the quorum variant alone can
// take several minutes, so a run against a real broker needs an explicit
// -timeout well above go test's default 10m; -timeout 30m is recommended.
func TestConformance(t *testing.T) {
	amqpURL := os.Getenv("TP_TEST_AMQP_URL")
	mgmtURL := os.Getenv("TP_TEST_RABBITMQ_MGMT_URL")

	if amqpURL == "" || mgmtURL == "" {
		h := &enginetest.Harness{
			Skip: func() (string, bool) {
				return "TP_TEST_AMQP_URL and TP_TEST_RABBITMQ_MGMT_URL must both be set", true
			},
		}
		enginetest.RunConformance(t, h)
		return
	}

	t.Run("Classic", func(t *testing.T) {
		// Name-isolated from the Quorum subtest (variant-scoped source name,
		// QueueName, ActivationID, and its own connection), so the two are
		// safe to interleave.
		t.Parallel()
		runConformanceVariant(t, amqpURL, mgmtURL, "classic", nil)
	})
	t.Run("Quorum", func(t *testing.T) {
		// Name-isolated from the Classic subtest (variant-scoped source name,
		// QueueName, ActivationID, and its own connection), so the two are
		// safe to interleave.
		t.Parallel()
		runConformanceVariant(t, amqpURL, mgmtURL, "quorum", amqp.Table{"x-queue-type": "quorum"})
	})
}

// runConformanceVariant runs the shared enginetest.RunConformance suite once,
// against a source queue declared with sourceArgs, under an identity and
// source name scoped by variant ("classic" or "quorum") so the two subtests
// never collide on the same broker resources.
func runConformanceVariant(t *testing.T, amqpURL, mgmtURL, variant string, sourceArgs amqp.Table) {
	t.Helper()

	conn, err := amqp.Dial(amqpURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	sourceName := fmt.Sprintf("tp-conformance-source-%s-%d", variant, time.Now().UnixNano())
	setupCh, err := conn.Channel()
	require.NoError(t, err)
	_, err = setupCh.QueueDeclare(sourceName, true, false, false, false, sourceArgs)
	require.NoError(t, err)
	require.NoError(t, setupCh.Close())
	t.Cleanup(func() {
		if ch, err := conn.Channel(); err == nil {
			_, _ = ch.QueueDelete(sourceName, false, false, false)
			_ = ch.Close()
		}
	})

	identity := enginetest.Identity{
		InstallID:    "conformance-install",
		WorkloadUID:  "conformance-workload",
		QueueName:    "conformance-queue-" + variant,
		ActivationID: fmt.Sprintf("conformance-%s-%d", variant, time.Now().UnixNano()),
		SourceEnv:    "SOURCE_QUEUE",
		HasGroups:    false,
	}

	h := &enginetest.Harness{
		Probe:    &probe{conn: conn, mgmt: newMgmtClient(mgmtURL, vhostOf(amqpURL)), source: sourceName},
		Identity: identity,
		NewEngine: func(*testing.T) engine.Engine {
			return New(Config{
				InstallID:     identity.InstallID,
				WorkloadUID:   identity.WorkloadUID,
				QueueName:     identity.QueueName,
				ActivationID:  identity.ActivationID,
				URL:           amqpURL,
				ManagementURL: mgmtURL,
				Source:        sourceName,
				SourceEnvName: identity.SourceEnv,
			})
		},
	}

	enginetest.RunConformance(t, h)
}

// probe implements enginetest.Probe against a real broker, independently of
// any Engine under test: its own connection, its own consumers, its own
// management client.
type probe struct {
	conn   *amqp.Connection
	mgmt   *mgmtClient
	source string
}

// SeedSource publishes msgs to the source queue with publisher confirms. The
// source always exists by the time a scenario seeds it, so plain (not
// mandatory) publishes are enough here.
func (p *probe) SeedSource(ctx context.Context, msgs []enginetest.Message) error {
	ch, err := p.conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Confirm(false); err != nil {
		return err
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, len(msgs)+1))

	for _, m := range msgs {
		if err := ch.PublishWithContext(ctx, "", p.source, false, false, toPublishing(m)); err != nil {
			return fmt.Errorf("seed publish %q: %w", m.ID, err)
		}
	}
	for range msgs {
		select {
		case c, ok := <-confirms:
			if !ok {
				return fmt.Errorf("seed: confirms channel closed early")
			}
			if !c.Ack {
				return fmt.Errorf("seed: publish nacked by broker")
			}
		case <-time.After(seedConfirmWindow):
			return fmt.Errorf("seed: timed out waiting for publish confirms")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// SetAppConsumed consumes and acks the first n messages published to the
// source, as the application's pre-split identity would have.
func (p *probe) SetAppConsumed(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	ch, err := p.conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Qos(n, 0, false); err != nil {
		return err
	}
	tag := uniqueTag("app-preconsume")
	deliveries, err := ch.Consume(p.source, tag, false, false, false, false, nil)
	if err != nil {
		return err
	}
	defer func() { _ = ch.Cancel(tag, false) }()

	for i := 0; i < n; i++ {
		select {
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("SetAppConsumed: source consumer closed after %d/%d", i, n)
			}
			if err := d.Ack(false); err != nil {
				return err
			}
		case <-time.After(probeTimeout):
			return fmt.Errorf("SetAppConsumed: timed out at message %d/%d", i+1, n)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// ReadShadow consumes up to expect messages from shadow with autoAck=true, a
// read observer that never re-observes since the broker considers each
// delivery settled the instant it is sent. It polls with basic.get, one
// message per call, rather than basic.consume: prefetch/QoS only bounds a
// manual-ack consumer's unacknowledged window, so an auto-ack
// basic.consume has no concept of "unacknowledged" for it to bound and the
// broker floods the channel with every ready message regardless of any Qos
// call. Reading more than expect that way would auto-ack (and so silently
// discard) messages this call was never asked to consume.
func (p *probe) ReadShadow(ctx context.Context, shadow string, expect int, timeout time.Duration) ([]enginetest.Message, error) {
	ch, err := p.conn.Channel()
	if err != nil {
		return nil, err
	}
	defer ch.Close()

	deadline := time.Now().Add(timeout)
	out := make([]enginetest.Message, 0, expect)
	for len(out) < expect {
		d, ok, err := ch.Get(shadow, true)
		if err != nil {
			return out, err
		}
		if ok {
			out = append(out, fromDelivery(d))
			continue
		}
		if time.Now().After(deadline) {
			return out, nil
		}
		select {
		case <-time.After(getPollInterval):
		case <-ctx.Done():
			return out, ctx.Err()
		}
	}
	return out, nil
}

// AppDrainShadow consumes shadow to empty with manual acks, committing as it
// goes, the way the redirected application would.
func (p *probe) AppDrainShadow(ctx context.Context, shadow string) (int, error) {
	ch, err := p.conn.Channel()
	if err != nil {
		return 0, err
	}
	defer ch.Close()
	if err := ch.Qos(probeDrainQos, 0, false); err != nil {
		return 0, err
	}
	tag := uniqueTag("app-drain")
	deliveries, err := ch.Consume(shadow, tag, false, false, false, false, nil)
	if err != nil {
		return 0, err
	}

	count := 0
	for {
		select {
		case d, ok := <-deliveries:
			if !ok {
				return count, nil
			}
			if err := d.Ack(false); err != nil {
				_ = ch.Cancel(tag, false)
				return count, err
			}
			count++
		case <-time.After(probeIdleTimeout):
			_ = ch.Cancel(tag, false)
			return count, nil
		case <-ctx.Done():
			_ = ch.Cancel(tag, false)
			return count, ctx.Err()
		}
	}
}

// ConsumeSourceAsApp consumes and acks up to limit messages from the source,
// as the application's restored identity, to prove the resume position after
// handback.
func (p *probe) ConsumeSourceAsApp(ctx context.Context, limit int, timeout time.Duration) ([]enginetest.Message, error) {
	ch, err := p.conn.Channel()
	if err != nil {
		return nil, err
	}
	defer ch.Close()
	if err := ch.Qos(limit, 0, false); err != nil {
		return nil, err
	}
	tag := uniqueTag("app-resumed")
	deliveries, err := ch.Consume(p.source, tag, false, false, false, false, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = ch.Cancel(tag, false) }()

	deadline := time.After(timeout)
	out := make([]enginetest.Message, 0, limit)
	for len(out) < limit {
		select {
		case d, ok := <-deliveries:
			if !ok {
				return out, nil
			}
			if err := d.Ack(false); err != nil {
				return out, err
			}
			out = append(out, fromDelivery(d))
		case <-deadline:
			return out, nil
		case <-ctx.Done():
			return out, ctx.Err()
		}
	}
	return out, nil
}

// ShadowExists reports whether shadow currently exists, via the management
// API (a 404 means false).
func (p *probe) ShadowExists(ctx context.Context, shadow string) (bool, error) {
	return p.mgmt.queueExists(ctx, shadow)
}

// ShadowDepth returns shadow's message count via an AMQP passive queue
// inspect, not the management API: management statistics are collected
// periodically and lag AMQP-visible state (as monitorQuorumSource's polling
// interval already accounts for), so a read taken right after a state
// transition can under-report and then "catch up" later with no new message
// having arrived, violating the never-increases-otherwise contract. A
// passive inspect queries the queue directly and neither consumes nor
// alters anything. A missing shadow reads as 0, not an error: nothing has
// reached it yet.
func (p *probe) ShadowDepth(_ context.Context, shadow string) (int64, error) {
	ch, err := p.conn.Channel()
	if err != nil {
		return 0, err
	}
	defer ch.Close()

	q, err := ch.QueueDeclarePassive(shadow, false, false, false, false, nil)
	if err != nil {
		var amqpErr *amqp.Error
		if errors.As(err, &amqpErr) && amqpErr.Code == amqp.NotFound {
			return 0, nil
		}
		return 0, err
	}
	return int64(q.Messages), nil
}

// toPublishing encodes m as an amqp.Publishing, stamping its ID into
// testIDHeaderKey alongside m's own filter headers.
func toPublishing(m enginetest.Message) amqp.Publishing {
	headers := make(amqp.Table, len(m.Headers)+1)
	for k, v := range m.Headers {
		headers[k] = v
	}
	headers[testIDHeaderKey] = m.ID
	return amqp.Publishing{
		Headers:      headers,
		Body:         m.Body,
		DeliveryMode: amqp.Persistent,
	}
}

// fromDelivery decodes d back into a Message, stripping testIDHeaderKey out
// of the reported Headers.
func fromDelivery(d amqp.Delivery) enginetest.Message {
	id, _ := d.Headers[testIDHeaderKey].(string)
	headers := make(map[string]string, len(d.Headers))
	for k, v := range d.Headers {
		if k == testIDHeaderKey {
			continue
		}
		if s, ok := v.(string); ok {
			headers[k] = s
		}
	}
	return enginetest.Message{ID: id, Headers: headers, Body: d.Body}
}

// uniqueTag returns a consumer tag scoped to this process run, so repeated
// probe calls on the same connection never collide.
func uniqueTag(kind string) string {
	return fmt.Sprintf("tp-probe-%s-%d", kind, time.Now().UnixNano())
}
