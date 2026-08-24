package kafkaintercept

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Route maps every source topic to the session shadow selected by Predicate.
type Route struct {
	ID        string
	Predicate Predicate
	Topics    map[string]string
}

// RoutingTable is adopted atomically between source transactions.
type RoutingTable struct {
	Generation uint64
	Paused     bool
	Routes     []Route
}

// SplitterConfig configures one stable member of an original application
// consumer group.
type SplitterConfig struct {
	Brokers            []string
	Group              string
	InstanceID         string
	TransactionalID    string
	AppTopics          map[string]string
	OffsetReset        string
	BatchSize          int
	TransactionTimeout time.Duration
	ClientOptions      []kgo.Opt
	InitialRoutes      RoutingTable
	OnGeneration       func(uint64)
}

// Splitter transactionally moves original-group records to personal or
// application shadow topics.
type Splitter struct {
	cfg     SplitterConfig
	session *kgo.GroupTransactSession

	routingMu sync.Mutex
	desired   atomic.Pointer[RoutingTable]
	wake      chan struct{}
	closeOnce sync.Once
}

// NewSplitter constructs a splitter without waiting for broker assignment.
func NewSplitter(cfg SplitterConfig) (*Splitter, error) {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.TransactionTimeout <= 0 {
		cfg.TransactionTimeout = 30 * time.Second
	}
	if err := validateSplitterConfig(cfg); err != nil {
		return nil, err
	}
	initial := cloneRoutingTable(cfg.InitialRoutes)
	if err := validateRoutingTable(initial, cfg.AppTopics); err != nil {
		return nil, err
	}
	opts := slices.Clone(cfg.ClientOptions)
	topics := slices.Sorted(maps.Keys(cfg.AppTopics))
	reset := kgo.NewOffset().AtStart()
	if cfg.OffsetReset == "latest" {
		reset = kgo.NewOffset().AtEnd()
	}
	opts = append(opts,
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(topics...),
		kgo.ConsumeResetOffset(reset),
		kgo.InstanceID(cfg.InstanceID),
		kgo.DisableAutoCommit(),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.TransactionalID(cfg.TransactionalID),
		kgo.TransactionTimeout(cfg.TransactionTimeout),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.Balancers(kgo.CooperativeStickyBalancer()),
	)
	session, err := kgo.NewGroupTransactSession(opts...)
	if err != nil {
		return nil, fmt.Errorf("create Kafka transaction session: %w", err)
	}
	s := &Splitter{cfg: cfg, session: session, wake: make(chan struct{}, 1)}
	s.desired.Store(&initial)
	return s, nil
}

func validateSplitterConfig(cfg SplitterConfig) error {
	switch {
	case len(cfg.Brokers) == 0:
		return errors.New("kafka splitter requires at least one broker")
	case cfg.Group == "":
		return errors.New("kafka splitter requires an original consumer group")
	case cfg.InstanceID == "":
		return errors.New("kafka splitter requires a stable instance ID")
	case cfg.TransactionalID == "":
		return errors.New("kafka splitter requires a stable transactional ID")
	case len(cfg.AppTopics) == 0:
		return errors.New("kafka splitter requires at least one source topic")
	case cfg.OffsetReset != "earliest" && cfg.OffsetReset != "latest":
		return fmt.Errorf("kafka splitter offset reset must be earliest or latest, got %q", cfg.OffsetReset)
	}
	for source, shadow := range cfg.AppTopics {
		if source == "" || shadow == "" {
			return errors.New("kafka splitter topic mappings must not be empty")
		}
	}
	return nil
}

// ApplyRoutingTable publishes a desired generation for adoption at the next
// transaction boundary.
func (s *Splitter) ApplyRoutingTable(table RoutingTable) error {
	s.routingMu.Lock()
	current := s.desired.Load()
	if table.Generation < current.Generation {
		s.routingMu.Unlock()
		return fmt.Errorf("kafka routing generation %d is older than %d", table.Generation, current.Generation)
	}
	if table.Generation == current.Generation {
		conflict := !reflect.DeepEqual(table, *current)
		s.routingMu.Unlock()
		if conflict {
			return fmt.Errorf("kafka routing generation %d has conflicting content", table.Generation)
		}
		return nil
	}
	table = cloneRoutingTable(table)
	if err := validateRoutingTable(table, s.cfg.AppTopics); err != nil {
		s.routingMu.Unlock()
		return err
	}
	s.desired.Store(&table)
	s.routingMu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func validateRoutingTable(table RoutingTable, appTopics map[string]string) error {
	for i := range table.Routes {
		route := &table.Routes[i]
		if route.ID == "" {
			return errors.New("kafka route ID must not be empty")
		}
		if err := route.Predicate.Validate(); err != nil {
			return fmt.Errorf("kafka route %q: %w", route.ID, err)
		}
		for source := range appTopics {
			if route.Topics[source] == "" {
				return fmt.Errorf("kafka route %q has no shadow for source topic %q", route.ID, source)
			}
		}
		for j := 0; j < i; j++ {
			if route.ID == table.Routes[j].ID {
				return fmt.Errorf("duplicate Kafka route ID %q", route.ID)
			}
			if route.Predicate.Overlaps(table.Routes[j].Predicate) {
				return fmt.Errorf("kafka routes %q and %q overlap", table.Routes[j].ID, route.ID)
			}
		}
	}
	return nil
}

func cloneRoutingTable(table RoutingTable) RoutingTable {
	table.Routes = slices.Clone(table.Routes)
	for i := range table.Routes {
		table.Routes[i].Predicate.Headers = cloneBytesMap(table.Routes[i].Predicate.Headers)
		table.Routes[i].Predicate.Key = slices.Clone(table.Routes[i].Predicate.Key)
		table.Routes[i].Predicate.KeyPrefix = slices.Clone(table.Routes[i].Predicate.KeyPrefix)
		table.Routes[i].Topics = maps.Clone(table.Routes[i].Topics)
	}
	return table
}

func cloneBytesMap(source map[string][]byte) map[string][]byte {
	if source == nil {
		return nil
	}
	result := make(map[string][]byte, len(source))
	for key, value := range source {
		result[key] = slices.Clone(value)
	}
	return result
}

// Run processes source records until ctx ends or a fatal broker error occurs.
func (s *Splitter) Run(ctx context.Context) error {
	defer s.Close()
	generation := ^uint64(0)
	var active RoutingTable
	for {
		desired := s.desired.Load()
		if desired.Generation != generation {
			active = *desired
			generation = active.Generation
			if s.cfg.OnGeneration != nil {
				s.cfg.OnGeneration(generation)
			}
		}
		if active.Paused {
			select {
			case <-ctx.Done():
				return nil
			case <-s.wake:
				continue
			}
		}

		pollCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		fetches := s.session.PollRecords(pollCtx, s.cfg.BatchSize)
		pollErr := pollCtx.Err()
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if pollErr != nil && len(fetches.Records()) == 0 {
			continue
		}
		if err := fetches.Err(); err != nil {
			return fmt.Errorf("poll Kafka source records: %w", err)
		}
		records := fetches.Records()
		if len(records) == 0 {
			continue
		}
		if err := s.transact(ctx, active, records); err != nil {
			return err
		}
	}
}

func (s *Splitter) transact(ctx context.Context, table RoutingTable, records []*kgo.Record) error {
	if err := s.session.Begin(); err != nil {
		return fmt.Errorf("begin Kafka split transaction: %w", err)
	}
	outputs := make([]*kgo.Record, len(records))
	for i, record := range records {
		destination := s.cfg.AppTopics[record.Topic]
		for _, route := range table.Routes {
			if route.Predicate.Matches(record) {
				destination = route.Topics[record.Topic]
				break
			}
		}
		outputs[i] = &kgo.Record{
			Topic: destination, Partition: record.Partition,
			Key: record.Key, Value: record.Value, Headers: record.Headers, Timestamp: record.Timestamp,
		}
	}
	if err := s.session.ProduceSync(ctx, outputs...).FirstErr(); err != nil {
		_, _ = s.session.End(context.WithoutCancel(ctx), kgo.TryAbort)
		return fmt.Errorf("produce Kafka shadow records: %w", err)
	}
	if _, err := s.session.End(ctx, kgo.TryCommit); err != nil {
		return fmt.Errorf("commit Kafka split transaction: %w", err)
	}
	return nil
}

// Close leaves the source group and closes all Kafka connections.
func (s *Splitter) Close() {
	s.closeOnce.Do(s.session.Close)
}
