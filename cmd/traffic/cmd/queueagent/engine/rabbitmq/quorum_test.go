package rabbitmq

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// testQuorumBroker skips the calling test unless a real broker is
// reachable, and returns the shared connection plus a fresh quorum source
// queue name, cleaned up on test exit.
func testQuorumBroker(t *testing.T, tag string) (*amqp.Connection, string) {
	t.Helper()
	amqpURL := os.Getenv("TP_TEST_AMQP_URL")
	mgmtURL := os.Getenv("TP_TEST_RABBITMQ_MGMT_URL")
	if amqpURL == "" || mgmtURL == "" {
		t.Skip("TP_TEST_AMQP_URL and TP_TEST_RABBITMQ_MGMT_URL must both be set")
	}

	conn, err := amqp.Dial(amqpURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	sourceName := fmt.Sprintf("tp-quorum-%s-source-%d", tag, time.Now().UnixNano())
	setupCh, err := conn.Channel()
	require.NoError(t, err)
	_, err = setupCh.QueueDeclare(sourceName, true, false, false, false,
		amqp.Table{"x-queue-type": "quorum"})
	require.NoError(t, err)
	require.NoError(t, setupCh.Close())
	t.Cleanup(func() {
		if ch, err := conn.Channel(); err == nil {
			_, _ = ch.QueueDelete(sourceName, false, false, false)
			_ = ch.Close()
		}
	})

	return conn, sourceName
}

// consumersOf reads shadow's live consumer count via a fresh channel's
// passive declare, the same live read resolveShadow uses.
func consumersOf(conn *amqp.Connection, shadow string) (int, error) {
	ch, err := conn.Channel()
	if err != nil {
		return 0, err
	}
	defer ch.Close()
	q, err := ch.QueueDeclarePassive(shadow, false, false, false, false, nil)
	if err != nil {
		return 0, err
	}
	return q.Consumers, nil
}

// TestQuorumParkedDeliveryBlocksCleanup proves both the pre-settlement
// refusal and the post-settlement NeedsDrainError path for a quorum app
// shadow: a delivery parked on an open basic.get hold blocks both
// VerifyCleanupReady and Cleanup (the hold reads as a live consumer);
// requeued after release, it surfaces as NeedsDrainError; drained and
// acked, Cleanup succeeds with the shadow retained.
func TestQuorumParkedDeliveryBlocksCleanup(t *testing.T) {
	conn, sourceName := testQuorumBroker(t, "parked")
	ctx := t.Context()

	eng := New(Config{
		InstallID:     "quorum-parked-install",
		WorkloadUID:   "quorum-parked-workload",
		QueueName:     "quorum-parked-queue",
		ActivationID:  fmt.Sprintf("quorum-parked-%d", time.Now().UnixNano()),
		URL:           os.Getenv("TP_TEST_AMQP_URL"),
		ManagementURL: os.Getenv("TP_TEST_RABBITMQ_MGMT_URL"),
		Source:        sourceName,
		SourceEnvName: "SOURCE_QUEUE",
	})
	t.Cleanup(func() { _ = eng.Close() })

	_, err := eng.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, "quorum", eng.sourceType)
	appShadow := eng.appShadowName

	pubCh, err := conn.Channel()
	require.NoError(t, err)
	require.NoError(t, pubCh.Confirm(false))
	confirms := pubCh.NotifyPublish(make(chan amqp.Confirmation, 1))
	require.NoError(t, pubCh.PublishWithContext(ctx, "", appShadow, false, false, amqp.Publishing{
		Body:         []byte("parked"),
		DeliveryMode: amqp.Persistent,
	}))
	select {
	case c := <-confirms:
		require.True(t, c.Ack, "publish to app shadow was nacked")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish confirm")
	}
	require.NoError(t, pubCh.Close())

	// Park it unacked: Get without acking, on a channel kept open. Closing
	// it would requeue the message, so it stays open until release below.
	holdCh, err := conn.Channel()
	require.NoError(t, err)
	_, ok, err := holdCh.Get(appShadow, false)
	require.NoError(t, err)
	require.True(t, ok, "expected the message published directly to the app shadow")

	require.Eventually(t, func() bool {
		n, err := consumersOf(conn, appShadow)
		return err == nil && n == 1
	}, 5*time.Second, 100*time.Millisecond, "shadow consumer count never reflected the held delivery")

	require.Error(t, eng.VerifyCleanupReady(ctx), "VerifyCleanupReady must refuse while the delivery is parked")

	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = eng.Cleanup(cleanupCtx)
	require.Error(t, err, "Cleanup must refuse to resolve a shadow with a live consumer")

	checkCh, err := conn.Channel()
	require.NoError(t, err)
	_, err = checkCh.QueueDeclarePassive(appShadow, false, false, false, false, nil)
	require.NoError(t, err, "app shadow must still exist after a refused Cleanup")
	require.NoError(t, checkCh.Close())

	// Release: close the holding channel, which requeues the message, then
	// wait for both the ready count and the consumer count to settle.
	require.NoError(t, holdCh.Close())
	require.Eventually(t, func() bool {
		messages, err := eng.passiveMessages(appShadow)
		return err == nil && messages == 1
	}, 5*time.Second, 100*time.Millisecond, "requeued message never reappeared as ready")
	require.Eventually(t, func() bool {
		n, err := consumersOf(conn, appShadow)
		return err == nil && n == 0
	}, 5*time.Second, 100*time.Millisecond, "shadow consumer count never settled back to 0 after requeue")

	var drainErr *engine.NeedsDrainError
	err = eng.VerifyCleanupReady(ctx)
	require.ErrorAs(t, err, &drainErr, "VerifyCleanupReady must report residue once the hold settles")
	require.Equal(t, appShadow, drainErr.Queue)
	require.Equal(t, 1, drainErr.Ready)

	// Drain the message for real.
	drainCh, err := conn.Channel()
	require.NoError(t, err)
	d, ok, err := drainCh.Get(appShadow, false)
	require.NoError(t, err)
	require.True(t, ok, "expected the requeued message")
	require.NoError(t, d.Ack(false))
	require.NoError(t, drainCh.Close())

	// The ack settles the delivery, but the shadow's live consumer count
	// trails that settlement by a short, asynchronous beat on a quorum
	// queue.
	require.Eventually(t, func() bool {
		n, err := consumersOf(conn, appShadow)
		return err == nil && n == 0
	}, 5*time.Second, 100*time.Millisecond, "shadow consumer count never settled back to 0 after final ack")

	require.NoError(t, eng.VerifyCleanupReady(ctx))

	retained, err := eng.Cleanup(ctx)
	require.NoError(t, err)
	require.Len(t, retained, 1)
	require.Equal(t, appShadow, retained[0].Name)
	require.NotEmpty(t, retained[0].Reason)

	existsCh, err := conn.Channel()
	require.NoError(t, err)
	q, err := existsCh.QueueDeclarePassive(appShadow, false, false, false, false, nil)
	require.NoError(t, err, "retained app shadow must still exist")
	require.Zero(t, q.Messages)
	require.NoError(t, existsCh.Close())
}

// TestQuorumCleanupRefusesWithForeignConsumer proves VerifyCleanupReady and
// Cleanup both refuse while a foreign consumer is attached to a quorum app
// shadow -- the exclusive consume flag is not enforced on a quorum queue
// (phase 0, R1), so nothing else stands between it and a live passive
// declare. Once cancelled, Cleanup succeeds with the shadow retained.
func TestQuorumCleanupRefusesWithForeignConsumer(t *testing.T) {
	conn, sourceName := testQuorumBroker(t, "foreign")
	ctx := t.Context()

	eng := New(Config{
		InstallID:     "quorum-foreign-install",
		WorkloadUID:   "quorum-foreign-workload",
		QueueName:     "quorum-foreign-queue",
		ActivationID:  fmt.Sprintf("quorum-foreign-%d", time.Now().UnixNano()),
		URL:           os.Getenv("TP_TEST_AMQP_URL"),
		ManagementURL: os.Getenv("TP_TEST_RABBITMQ_MGMT_URL"),
		Source:        sourceName,
		SourceEnvName: "SOURCE_QUEUE",
	})
	t.Cleanup(func() { _ = eng.Close() })

	_, err := eng.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, "quorum", eng.sourceType)
	appShadow := eng.appShadowName

	foreignCh, err := conn.Channel()
	require.NoError(t, err)
	foreignTag := "tp-foreign-consumer"
	_, err = foreignCh.Consume(appShadow, foreignTag, true, false, false, false, nil)
	require.NoError(t, err)

	require.Error(t, eng.VerifyCleanupReady(ctx), "VerifyCleanupReady must refuse while a foreign consumer is attached")

	cleanupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = eng.Cleanup(cleanupCtx)
	require.Error(t, err, "Cleanup must refuse to resolve a shadow with a foreign consumer attached")

	checkCh, err := conn.Channel()
	require.NoError(t, err)
	_, err = checkCh.QueueDeclarePassive(appShadow, false, false, false, false, nil)
	require.NoError(t, err, "app shadow must still exist after a refused Cleanup")
	require.NoError(t, checkCh.Close())

	require.NoError(t, foreignCh.Cancel(foreignTag, false))
	require.NoError(t, foreignCh.Close())

	require.NoError(t, eng.VerifyCleanupReady(ctx))
	retained, err := eng.Cleanup(ctx)
	require.NoError(t, err)
	require.Len(t, retained, 1)
	require.Equal(t, appShadow, retained[0].Name)
}

// TestQuorumOrderlyStopStaysHealthy proves that an orderly Stop on a quorum
// source does not trip the foreign-consumer monitor: after Stop cancels the
// engine's own consumer, the source legitimately reports zero consumers, and
// a monitor left running would misread that as an ownership violation on
// its next tick. Cleanup afterward must succeed with the app shadow
// retained.
func TestQuorumOrderlyStopStaysHealthy(t *testing.T) {
	_, sourceName := testQuorumBroker(t, "stop")
	ctx := t.Context()

	eng := New(Config{
		InstallID:     "quorum-install",
		WorkloadUID:   "quorum-workload",
		QueueName:     "quorum-queue",
		ActivationID:  fmt.Sprintf("quorum-%d", time.Now().UnixNano()),
		URL:           os.Getenv("TP_TEST_AMQP_URL"),
		ManagementURL: os.Getenv("TP_TEST_RABBITMQ_MGMT_URL"),
		Source:        sourceName,
		SourceEnvName: "SOURCE_QUEUE",
	})
	t.Cleanup(func() { _ = eng.Close() })

	_, err := eng.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng.Start(ctx))

	handoff, err := eng.Stop(ctx)
	require.NoError(t, err)
	require.NotNil(t, handoff)

	// Outlast two monitor ticks: a monitor that survived Stop would read the
	// now consumer-less source and mark the engine unhealthy.
	time.Sleep(2*mgmtPollInterval*20 + time.Second)
	st := eng.Status(ctx)
	require.True(t, st.Healthy, "engine went unhealthy after orderly Stop: %s", st.Detail)

	require.NoError(t, eng.VerifyCleanupReady(ctx))
	retained, err := eng.Cleanup(ctx)
	require.NoError(t, err)
	require.Len(t, retained, 1)
	require.Equal(t, eng.appShadowName, retained[0].Name)
}

// TestQuorumOrderlyAbortStaysHealthy proves that an orderly Abort on a
// quorum source does not trip the foreign-consumer monitor, the same way
// TestQuorumOrderlyStopStaysHealthy proves it for Stop. Cleanup afterward
// must succeed with the app shadow retained.
func TestQuorumOrderlyAbortStaysHealthy(t *testing.T) {
	_, sourceName := testQuorumBroker(t, "abort")
	ctx := t.Context()

	eng := New(Config{
		InstallID:     "quorum-install",
		WorkloadUID:   "quorum-workload",
		QueueName:     "quorum-queue",
		ActivationID:  fmt.Sprintf("quorum-abort-%d", time.Now().UnixNano()),
		URL:           os.Getenv("TP_TEST_AMQP_URL"),
		ManagementURL: os.Getenv("TP_TEST_RABBITMQ_MGMT_URL"),
		Source:        sourceName,
		SourceEnvName: "SOURCE_QUEUE",
	})
	t.Cleanup(func() { _ = eng.Close() })

	_, err := eng.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng.Start(ctx))

	require.NoError(t, eng.Abort(ctx))

	// Outlast two monitor ticks: a monitor that survived Abort would read the
	// now consumer-less source and mark the engine unhealthy.
	time.Sleep(2*mgmtPollInterval*20 + time.Second)
	st := eng.Status(ctx)
	require.True(t, st.Healthy, "engine went unhealthy after orderly Abort: %s", st.Detail)

	require.NoError(t, eng.VerifyCleanupReady(ctx))
	retained, err := eng.Cleanup(ctx)
	require.NoError(t, err)
	require.Len(t, retained, 1)
	require.Equal(t, eng.appShadowName, retained[0].Name)
}
