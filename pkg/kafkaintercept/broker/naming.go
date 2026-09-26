// Package broker owns Kafka-specific resource naming and lifecycle operations.
package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	validation "k8s.io/apimachinery/pkg/util/validation"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
)

const (
	maxKafkaTopicLength = 249
	maxKafkaIDLength    = 1<<15 - 1 // Kafka protocol STRING length.
)

// Names is the deterministic broker and Kubernetes inventory for one split.
type Names struct {
	SourceGroup           string
	ApplicationGroup      string
	SplitterInstance      string
	SplitterTransactional string
	KubernetesName        string
}

// ResourceNames derives names from the scopes that already identify a split.
func ResourceNames(object metav1.Object, group string) Names {
	return Names{
		SourceGroup:           group,
		ApplicationGroup:      group + ".tp-app",
		SplitterInstance:      "tp-splitter-",
		SplitterTransactional: group + ".tp-splitter-",
		KubernetesName:        object.GetNamespace() + "-" + object.GetName(),
	}
}

// ValidateApplication checks all names needed before application shadow
// creation.
func (n Names) ValidateApplication(topics []string, replicas int32, managed, applicationTransactions bool) error {
	var errs []error
	if managed {
		errs = append(errs, validateKafkaName("application group", n.ApplicationGroup, maxKafkaIDLength))
		for _, topic := range topics {
			errs = append(errs, validateKafkaTopic("application topic", n.ApplicationTopic(topic)))
		}
	}
	if applicationTransactions {
		errs = append(errs, validateKafkaName("application transaction", n.ApplicationTransactional()+"."+strings.Repeat("0", 36), maxKafkaIDLength))
	}
	ordinal := max(replicas-1, 0)
	errs = append(errs,
		validateKafkaName("splitter instance", fmt.Sprintf("%s%d", n.SplitterInstance, ordinal), maxKafkaIDLength),
		validateKafkaName("splitter transaction", fmt.Sprintf("%s%d", n.SplitterTransactional, ordinal), maxKafkaIDLength),
	)
	if messages := validation.IsDNS1123Label(n.KubernetesName); len(messages) > 0 {
		errs = append(errs, fmt.Errorf("splitter resource name %q is invalid: %s; shorten the namespace or KafkaSplit name", n.KubernetesName, strings.Join(messages, ", ")))
	}
	return errors.Join(errs...)
}

// ValidateSession checks all names needed before personal shadow creation.
func (n Names) ValidateSession(route string, topics []string, managed, clientTransactions bool) error {
	var errs []error
	if managed {
		errs = append(errs, validateKafkaName("session group", n.SessionGroup(route), maxKafkaIDLength))
		for _, topic := range topics {
			errs = append(errs, validateKafkaTopic("session topic", n.SessionTopic(route, topic)))
		}
	}
	errs = append(errs, validateKafkaName("session drain transaction", n.DrainTransactional(route), maxKafkaIDLength))
	if clientTransactions {
		errs = append(errs, validateKafkaName("personal client transaction", n.ClientTransactional(route), maxKafkaIDLength))
	}
	return errors.Join(errs...)
}

func validateKafkaName(kind, name string, limit int) error {
	if name == "" {
		return fmt.Errorf("generated Kafka %s is empty", kind)
	}
	if len(name) > limit {
		return fmt.Errorf(
			"generated Kafka %s is %d characters; shorten the source group, source topic, or route name to fit the %d-character limit",
			kind, len(name), limit,
		)
	}
	return nil
}

func validateKafkaTopic(kind, name string) error {
	if err := validateKafkaName(kind, name, maxKafkaTopicLength); err != nil {
		return err
	}
	if name == "." || name == ".." || strings.IndexFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
	}) >= 0 {
		return fmt.Errorf(
			"generated Kafka %s %q contains characters Kafka topics do not accept; "+
				"use a source group containing only ASCII letters, digits, '.', '_', or '-', or use preprovisioned shadows",
			kind, name,
		)
	}
	return nil
}

// OwnershipLeaseName identifies one cluster/group pair independent of its
// owning KafkaSplit.
func OwnershipLeaseName(split *api.KafkaSplit) string {
	servers := slices.Clone(split.Spec.Connection.BootstrapServers)
	slices.Sort(servers)
	return "kg-" + shortHash(strings.Join(append(servers, split.Spec.Source.Group), "\x00"))
}

// ApplicationTopic returns the managed application shadow for source.
func (n Names) ApplicationTopic(source string) string {
	return source + ".tp." + n.SourceGroup + ".app"
}

// SessionGroup returns the managed consumer group for a route.
func (n Names) SessionGroup(route string) string {
	return n.SourceGroup + ".tp." + route
}

// SessionTopic returns the managed session shadow for source and route.
func (n Names) SessionTopic(route, source string) string {
	return source + ".tp." + n.SourceGroup + "." + route
}

// ApplicationTransactional returns the application transaction ID base.
func (n Names) ApplicationTransactional() string {
	return n.SourceGroup + ".tp-app"
}

// ClientTransactional returns the personal consumer transaction ID.
func (n Names) ClientTransactional(route string) string {
	return n.SourceGroup + ".tp-client." + route
}

// DrainTransactional returns the session drain transaction ID.
func (n Names) DrainTransactional(route string) string {
	return n.SourceGroup + ".tp-drain." + route
}

// DrainInstance returns the stable group-member instance ID used while
// draining a route's session.
func (n Names) DrainInstance(route string) string {
	return "tp-drain-" + route
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}
