package kafka

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// baseClientOpts returns the broker connection options common to every
// client this engine builds.
func (e *Engine) baseClientOpts() ([]kgo.Opt, error) {
	opts := []kgo.Opt{kgo.SeedBrokers(e.cfg.Brokers...)}
	if e.cfg.TLS != nil {
		opts = append(opts, kgo.DialTLSConfig(e.cfg.TLS))
	}
	mech, err := e.saslMechanism()
	if err != nil {
		return nil, err
	}
	if mech != nil {
		opts = append(opts, kgo.SASL(mech))
	}
	return opts, nil
}

// saslMechanism builds the SASL mechanism cfg requests, or returns nil for a
// broker with no SASL configured.
func (e *Engine) saslMechanism() (sasl.Mechanism, error) {
	switch e.cfg.SASLMechanism {
	case "":
		return nil, nil
	case "PLAIN":
		return plain.Auth{User: e.cfg.SASLUser, Pass: e.cfg.SASLPassword}.AsMechanism(), nil
	case "SCRAM-SHA-256":
		return scram.Auth{User: e.cfg.SASLUser, Pass: e.cfg.SASLPassword}.AsSha256Mechanism(), nil
	case "SCRAM-SHA-512":
		return scram.Auth{User: e.cfg.SASLUser, Pass: e.cfg.SASLPassword}.AsSha512Mechanism(), nil
	default:
		return nil, fmt.Errorf("kafka: unsupported SASL mechanism %q", e.cfg.SASLMechanism)
	}
}

// ensureClients lazily builds the admin and transactional producer clients
// this engine holds for its whole lifetime. The caller must hold mu.
func (e *Engine) ensureClients() error {
	if e.closed {
		return errors.New("kafka: engine is closed")
	}
	if e.admin != nil {
		return nil
	}

	opts, err := e.baseClientOpts()
	if err != nil {
		return err
	}
	adminCl, err := kgo.NewClient(opts...)
	if err != nil {
		return fmt.Errorf("kafka: building admin client: %w", err)
	}
	e.admin = kadm.NewClient(adminCl)

	prodOpts := append(slices.Clone(opts),
		kgo.TransactionalID(e.splitterGroup),
		kgo.TransactionTimeout(30*time.Second),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
	)
	producer, err := kgo.NewClient(prodOpts...)
	if err != nil {
		e.closeClientsLocked()
		return fmt.Errorf("kafka: building producer client: %w", err)
	}
	e.producer = producer
	return nil
}
