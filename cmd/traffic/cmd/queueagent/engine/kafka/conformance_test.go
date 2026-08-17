package kafka

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine/enginetest"
	"github.com/telepresenceio/telepresence/v2/pkg/queuestate"
)

// pollRetryBackoff avoids a tight retry loop after a transient fetch error
// in the probe's polling helpers below.
const pollRetryBackoff = 20 * time.Millisecond

// Suite identity, set up once in TestMain from TP_TEST_KAFKA_BROKERS. Empty
// brokers means the suite is skipped.
var (
	brokers      []string
	sourceTopic  string
	appGroupName string
	identity     enginetest.Identity

	admin    *kadm.Client
	producer *kgo.Client
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	raw := os.Getenv("TP_TEST_KAFKA_BROKERS")
	if raw == "" {
		return m.Run()
	}
	brokers = strings.Split(raw, ",")

	if err := waitForBroker(brokers); err != nil {
		fmt.Fprintln(os.Stderr, "kafka conformance: broker never became ready:", err)
		return 1
	}

	adminCl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		fmt.Fprintln(os.Stderr, "kafka conformance: building admin client:", err)
		return 1
	}
	defer adminCl.Close()
	admin = kadm.NewClient(adminCl)

	prodCl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "kafka conformance: building producer client:", err)
		return 1
	}
	defer prodCl.Close()
	producer = prodCl

	ts := time.Now().UnixNano()
	sourceTopic = fmt.Sprintf("tp-conf-src-%d", ts)
	appGroupName = fmt.Sprintf("tp-conf-appgrp-%d", ts)
	identity = enginetest.Identity{
		InstallID:    "kafka-conformance",
		WorkloadUID:  fmt.Sprintf("wl-%d", ts),
		QueueName:    "orders",
		ActivationID: fmt.Sprintf("act-%d", ts),
		SourceEnv:    "ORDERS_TOPIC",
		GroupEnv:     "ORDERS_GROUP",
		HasGroups:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Single partition: the suite relies on strict source ordering.
	if _, err := admin.CreateTopic(ctx, 1, -1, nil, sourceTopic); err != nil {
		fmt.Fprintln(os.Stderr, "kafka conformance: creating source topic:", err)
		return 1
	}

	// A freshly started single-node broker answers metadata requests
	// before its group coordinator (backed by the lazily-created
	// __consumer_offsets topic) is ready. The engine does not retry
	// COORDINATOR_NOT_AVAILABLE itself, so warm the coordinator up here
	// with a harmless group offset read before any scenario runs.
	if err := warmUpGroupCoordinator(); err != nil {
		fmt.Fprintln(os.Stderr, "kafka conformance: group coordinator never became ready:", err)
		return 1
	}

	return m.Run()
}

// warmUpGroupCoordinator retries a harmless group offset fetch until the
// broker's group coordinator answers.
func warmUpGroupCoordinator() error {
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, lastErr = admin.FetchOffsetsForTopics(ctx, "tp-conf-warmup", sourceTopic)
		cancel()
		if lastErr == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return lastErr
}

// waitForBroker retries an admin metadata request until the broker answers,
// tolerating a freshly started container's slow startup window.
func waitForBroker(brokers []string) error {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return err
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)

	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, lastErr = adm.BrokerMetadata(ctx)
		cancel()
		if lastErr == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return lastErr
}

func TestConformance(t *testing.T) {
	p := &probe{}
	h := &enginetest.Harness{
		Identity: identity,
		Skip: func() (string, bool) {
			if len(brokers) == 0 {
				return "TP_TEST_KAFKA_BROKERS not set", true
			}
			return "", false
		},
		Probe:         p,
		BeginScenario: p.beginScenario,
		NewEngine: func(t *testing.T) engine.Engine {
			t.Helper()
			return New(Config{
				InstallID:     identity.InstallID,
				WorkloadUID:   identity.WorkloadUID,
				QueueName:     identity.QueueName,
				ActivationID:  identity.ActivationID,
				Brokers:       brokers,
				Source:        sourceTopic,
				Group:         appGroupName,
				OffsetReset:   "earliest",
				SourceEnvName: identity.SourceEnv,
				GroupEnvName:  identity.GroupEnv,
			})
		},
	}
	enginetest.RunConformance(t, h)
}

// probe implements enginetest.Probe against the package-level admin and
// producer clients set up in TestMain.
//
// caughtUp records whether the current scenario's SeedSource has already
// caught the app group up to the source's pre-scenario position:
// beginScenario resets it, a scenario's first SeedSource call catches up
// (hiding an earlier scenario's tail), and a later SeedSource call within
// the same scenario does not (keeping that scenario's own earlier batch
// visible to its own later app-group-relative reads).
type probe struct {
	mu       sync.Mutex
	caughtUp bool
}

// beginScenario resets caughtUp for a new scenario.
func (p *probe) beginScenario() {
	p.mu.Lock()
	p.caughtUp = false
	p.mu.Unlock()
}

func (p *probe) SeedSource(ctx context.Context, msgs []enginetest.Message) error {
	p.mu.Lock()
	already := p.caughtUp
	p.caughtUp = true
	p.mu.Unlock()
	if !already {
		if err := catchUpAppGroup(ctx); err != nil {
			return err
		}
	}
	for _, m := range msgs {
		rec := &kgo.Record{
			Topic:   sourceTopic,
			Key:     []byte(m.ID),
			Value:   m.Body,
			Headers: toKgoHeaders(m.Headers),
		}
		res := producer.ProduceSync(ctx, rec)
		if err := res.FirstErr(); err != nil {
			return fmt.Errorf("seeding source: %w", err)
		}
	}
	return nil
}

// catchUpAppGroup commits the app group's offset to the source topic's
// current end, on every partition.
func catchUpAppGroup(ctx context.Context) error {
	end, err := admin.ListEndOffsets(ctx, sourceTopic)
	if err != nil {
		return fmt.Errorf("listing source end offsets: %w", err)
	}
	if err := end.Error(); err != nil {
		return fmt.Errorf("listing source end offsets: %w", err)
	}
	var seed kadm.Offsets
	for p, o := range end[sourceTopic] {
		seed.AddOffset(sourceTopic, p, o.Offset, -1)
	}
	if len(seed) == 0 {
		return nil
	}
	resp, err := admin.CommitOffsets(ctx, appGroupName, seed)
	if err != nil {
		return fmt.Errorf("catching up app group: %w", err)
	}
	return resp.Error()
}

// SetAppConsumed commits the app group n positions past wherever it
// currently sits, rather than to the absolute offset n: the suite's
// scenarios share one app group, so "the first n messages" means the first
// n since this scenario's own catch-up point (see SeedSource), not n from
// the topic's start.
func (p *probe) SetAppConsumed(ctx context.Context, n int) error {
	current, err := admin.FetchOffsetsForTopics(ctx, appGroupName, sourceTopic)
	if err != nil {
		return fmt.Errorf("fetching app group offset: %w", err)
	}
	base := int64(0)
	if o, ok := current.Lookup(sourceTopic, 0); ok && o.Err == nil && o.At >= 0 {
		base = o.At
	}
	var seed kadm.Offsets
	seed.AddOffset(sourceTopic, 0, base+int64(n), -1)
	resp, err := admin.CommitOffsets(ctx, appGroupName, seed)
	if err != nil {
		return fmt.Errorf("committing app-consumed offset: %w", err)
	}
	return resp.Error()
}

func (p *probe) ReadShadow(
	ctx context.Context, shadow string, expect int, timeout time.Duration,
) ([]enginetest.Message, error) {
	observerGroup := "tp-conf-observer-" + shadow
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(observerGroup),
		kgo.ConsumeTopics(shadow),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return nil, fmt.Errorf("building shadow reader for %q: %w", shadow, err)
	}
	defer cl.Close()

	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var msgs []enginetest.Message
	for len(msgs) < expect {
		fetches := cl.PollRecords(readCtx, expect-len(msgs))
		if readCtx.Err() != nil {
			break
		}
		if err := fetches.Err(); err != nil {
			time.Sleep(pollRetryBackoff)
			continue
		}
		recs := fetches.Records()
		if len(recs) == 0 {
			continue
		}
		for _, r := range recs {
			msgs = append(msgs, enginetest.Message{ID: string(r.Key), Headers: headerMap(r.Headers), Body: r.Value})
		}
		if err := cl.CommitUncommittedOffsets(ctx); err != nil {
			return msgs, fmt.Errorf("committing shadow reader offsets: %w", err)
		}
	}
	return msgs, nil
}

func (p *probe) AppDrainShadow(ctx context.Context, shadow string) (int, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(queuestate.AppGroupName(identity.InstallID, identity.WorkloadUID, identity.QueueName, identity.ActivationID)),
		kgo.ConsumeTopics(shadow),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return 0, fmt.Errorf("building app-shadow drainer: %w", err)
	}
	defer cl.Close()

	admCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	end, err := admin.ListEndOffsets(admCtx, shadow)
	if err != nil {
		return 0, fmt.Errorf("listing app shadow end offsets: %w", err)
	}
	target := int64(0)
	if o, ok := end.Lookup(shadow, 0); ok {
		target = o.Offset
	}
	if target == 0 {
		return 0, nil
	}

	// target is a raw log offset: every transactionally produced record
	// shares its produce with a trailing control (marker) batch that also
	// consumes an offset but is never delivered as a Record, so a plain
	// count of consumed records can never reach it. LastStableOffset, a
	// raw position reported on each fetch, is the value directly
	// comparable to target.
	consumed := 0
	for {
		if ctx.Err() != nil {
			return consumed, ctx.Err()
		}
		fetches := cl.PollFetches(ctx)
		if err := fetches.Err(); err != nil {
			time.Sleep(pollRetryBackoff)
			continue
		}
		done := false
		fetches.EachPartition(func(fp kgo.FetchTopicPartition) {
			consumed += len(fp.Records)
			if fp.LastStableOffset >= target {
				done = true
			}
		})
		if len(fetches) > 0 {
			if err := cl.CommitUncommittedOffsets(ctx); err != nil {
				return consumed, fmt.Errorf("committing app-shadow drain offsets: %w", err)
			}
		}
		if done {
			return consumed, nil
		}
	}
}

func (p *probe) ConsumeSourceAsApp(ctx context.Context, limit int, timeout time.Duration) ([]enginetest.Message, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(appGroupName),
		kgo.ConsumeTopics(sourceTopic),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return nil, fmt.Errorf("building source consumer: %w", err)
	}
	defer cl.Close()

	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var msgs []enginetest.Message
	for len(msgs) < limit {
		fetches := cl.PollRecords(readCtx, limit-len(msgs))
		if readCtx.Err() != nil {
			break
		}
		if err := fetches.Err(); err != nil {
			time.Sleep(pollRetryBackoff)
			continue
		}
		recs := fetches.Records()
		if len(recs) == 0 {
			continue
		}
		for _, r := range recs {
			msgs = append(msgs, enginetest.Message{ID: string(r.Key), Headers: headerMap(r.Headers), Body: r.Value})
		}
		if err := cl.CommitUncommittedOffsets(ctx); err != nil {
			return msgs, fmt.Errorf("committing source consumer offsets: %w", err)
		}
	}
	return msgs, nil
}

// ShadowDepth sums shadow's raw per-partition end offsets: any produce --
// a record or a transaction marker -- advances one, and listing them reads
// the log without consuming anything. A missing topic reports depth 0.
func (p *probe) ShadowDepth(ctx context.Context, shadow string) (int64, error) {
	end, err := admin.ListEndOffsets(ctx, shadow)
	if err != nil {
		return 0, fmt.Errorf("listing shadow %q end offsets: %w", shadow, err)
	}
	var sum int64
	for _, o := range end[shadow] {
		if o.Err != nil {
			if isUnknownTopicErr(o.Err) {
				return 0, nil
			}
			return 0, fmt.Errorf("listing shadow %q end offsets: %w", shadow, o.Err)
		}
		sum += o.Offset
	}
	return sum, nil
}

func (p *probe) ShadowExists(ctx context.Context, shadow string) (bool, error) {
	topics, err := admin.ListTopics(ctx, shadow)
	if err != nil {
		return false, fmt.Errorf("listing topic %q: %w", shadow, err)
	}
	td, ok := topics[shadow]
	if !ok {
		return false, nil
	}
	if td.Err != nil {
		if isUnknownTopicErr(td.Err) {
			return false, nil
		}
		return false, fmt.Errorf("describing topic %q: %w", shadow, td.Err)
	}
	return true, nil
}
