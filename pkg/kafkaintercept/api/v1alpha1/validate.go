package v1alpha1

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	validation2 "k8s.io/apimachinery/pkg/util/validation"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
)

// Validate checks the complete KafkaSplit contract independently of admission.
func (s *KafkaSplit) Validate() error {
	var errs []error
	if s.Spec.DesiredState != DesiredStateEnabled && s.Spec.DesiredState != DesiredStateDisabled {
		errs = append(errs, fmt.Errorf("invalid desiredState %q", s.Spec.DesiredState))
	}
	if selectorErrs := validation.ValidateLabelSelector(&s.Spec.WorkloadSelector, validation.LabelSelectorValidationOptions{}, nil); len(selectorErrs) > 0 {
		errs = append(errs, selectorErrs.ToAggregate())
	}
	if messages := validation2.IsDNS1123Label(s.Spec.Container); len(messages) > 0 {
		errs = append(errs, fmt.Errorf("invalid container %q: %s", s.Spec.Container, strings.Join(messages, ", ")))
	}
	errs = append(errs, validateConnection(s.Spec.Connection)...)
	errs = append(errs, validateSource(s.Spec.Source)...)
	errs = append(errs, validateApplication(s.Spec.Source, s.Spec.Application)...)
	errs = append(errs, validateShadows(s.Spec.Source, s.Spec.Shadows)...)
	if s.Spec.Splitter.Replicas < 0 {
		errs = append(errs, errors.New("splitter replicas must not be negative"))
	}
	if s.Spec.Splitter.BatchSize < 0 {
		errs = append(errs, errors.New("splitter batchSize must not be negative"))
	}
	return errors.Join(errs...)
}

func validateConnection(connection KafkaConnectionSpec) []error {
	var errs []error
	if len(connection.BootstrapServers) == 0 {
		errs = append(errs, errors.New("connection.bootstrapServers must not be empty"))
	}
	for _, server := range connection.BootstrapServers {
		if _, _, err := net.SplitHostPort(server); err != nil {
			errs = append(errs, fmt.Errorf("invalid bootstrap server %q: %w", server, err))
		}
	}
	if tls := connection.TLS; tls != nil && (tls.Certificate == nil) != (tls.PrivateKey == nil) {
		errs = append(errs, errors.New("TLS certificate and privateKey must be specified together"))
	}
	if sasl := connection.SASL; sasl != nil {
		switch sasl.Mechanism {
		case "PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512":
			if sasl.Username == nil || sasl.Password == nil {
				errs = append(errs, fmt.Errorf("%s requires username and password", sasl.Mechanism))
			}
		case "OAUTHBEARER":
			if sasl.OAuth == nil {
				errs = append(errs, errors.New("OAUTHBEARER requires oauth configuration"))
			}
		case "GSSAPI":
			if sasl.Kerberos == nil {
				errs = append(errs, errors.New("GSSAPI requires kerberos configuration"))
			}
		case "AWS_MSK_IAM":
			if sasl.AWS == nil || sasl.AWS.Region == "" {
				errs = append(errs, errors.New("AWS_MSK_IAM requires an AWS region"))
			}
		default:
			errs = append(errs, fmt.Errorf("unsupported SASL mechanism %q", sasl.Mechanism))
		}
	}
	return errs
}

func validateSource(source KafkaSourceSpec) []error {
	var errs []error
	if source.Group == "" {
		errs = append(errs, errors.New("source.group must not be empty"))
	}
	if len(source.Topics) == 0 {
		errs = append(errs, errors.New("source.topics must not be empty"))
	}
	seen := make(map[string]struct{}, len(source.Topics))
	for _, topic := range source.Topics {
		if topic == "" {
			errs = append(errs, errors.New("source topic must not be empty"))
		} else if _, ok := seen[topic]; ok {
			errs = append(errs, fmt.Errorf("duplicate source topic %q", topic))
		}
		seen[topic] = struct{}{}
	}
	if source.OffsetReset != "earliest" && source.OffsetReset != "latest" {
		errs = append(errs, fmt.Errorf("invalid source.offsetReset %q", source.OffsetReset))
	}
	return errs
}

func validateApplication(source KafkaSourceSpec, application KafkaApplicationSpec) []error {
	var errs []error
	if (application.TopicEnv == "") == (len(application.TopicBindings) == 0) {
		errs = append(errs, errors.New("application must specify exactly one of topicEnv or topicBindings"))
	}
	envs := make(map[string]string)
	addEnv := func(field, name string) {
		if messages := validation2.IsEnvVarName(name); len(messages) > 0 {
			errs = append(errs, fmt.Errorf("invalid %s %q: %s", field, name, strings.Join(messages, ", ")))
			return
		}
		if prior, ok := envs[name]; ok {
			errs = append(errs, fmt.Errorf("environment variable %q is used by both %s and %s", name, prior, field))
		}
		envs[name] = field
	}
	if application.TopicEnv != "" {
		addEnv("topicEnv", application.TopicEnv)
		if application.TopicSeparator == "" {
			errs = append(errs, errors.New("application.topicSeparator must be set with topicEnv"))
		}
	} else {
		bound := make(map[string]struct{}, len(application.TopicBindings))
		for _, binding := range application.TopicBindings {
			if !slices.Contains(source.Topics, binding.Source) {
				errs = append(errs, fmt.Errorf("topic binding refers to unknown source %q", binding.Source))
			}
			if _, ok := bound[binding.Source]; ok {
				errs = append(errs, fmt.Errorf("duplicate topic binding for %q", binding.Source))
			}
			bound[binding.Source] = struct{}{}
			addEnv("topic binding", binding.Env)
		}
		for _, topic := range source.Topics {
			if _, ok := bound[topic]; !ok {
				errs = append(errs, fmt.Errorf("source topic %q has no application binding", topic))
			}
		}
	}
	addEnv("groupEnv", application.GroupEnv)
	addEnv("isolationLevelEnv", application.IsolationLevelEnv)
	if application.TransactionalIDEnv != "" {
		addEnv("transactionalIDEnv", application.TransactionalIDEnv)
	}
	for name, value := range application.ShadowCredentials {
		addEnv("shadowCredentials", name)
		if err := validateValueSource(value); err != nil {
			errs = append(errs, fmt.Errorf("shadow credential %q: %w", name, err))
		}
	}
	return errs
}

func validateValueSource(source ValueSource) error {
	if (source.Value == "") == (source.SecretKeyRef == nil) {
		return errors.New("must contain exactly one of value or secretKeyRef")
	}
	return nil
}

func validateShadows(source KafkaSourceSpec, shadows KafkaShadowSpec) []error {
	var errs []error
	switch shadows.Mode {
	case ShadowModeManaged:
		if shadows.Managed == nil || shadows.Preprovisioned != nil {
			errs = append(errs, errors.New("Managed shadows require only the managed configuration"))
		}
	case ShadowModePreprovisioned:
		if shadows.Preprovisioned == nil || shadows.Managed != nil {
			errs = append(errs, errors.New("Preprovisioned shadows require only the preprovisioned configuration"))
			break
		}
		pre := shadows.Preprovisioned
		topicOwners := make(map[string]string, len(source.Topics)+len(pre.ApplicationTopics)+len(pre.Sessions)*len(source.Topics))
		sourceTopics := make(map[string]struct{}, len(source.Topics))
		for _, topic := range source.Topics {
			topicOwners[topic] = "source topic"
			sourceTopics[topic] = struct{}{}
		}
		groupOwners := map[string]string{source.Group: "source group"}
		addUnique := func(owners map[string]string, name, owner, kind string) {
			if name == "" {
				return
			}
			if prior, ok := owners[name]; ok {
				errs = append(errs, fmt.Errorf("preprovisioned %s %q is used by both %s and %s", kind, name, prior, owner))
				return
			}
			owners[name] = owner
		}
		if pre.ApplicationGroup == "" {
			errs = append(errs, errors.New("preprovisioned applicationGroup must not be empty"))
		}
		addUnique(groupOwners, pre.ApplicationGroup, "application group", "group")
		for _, topic := range source.Topics {
			applicationTopic := pre.ApplicationTopics[topic]
			if applicationTopic == "" {
				errs = append(errs, fmt.Errorf("preprovisioned application topic missing for source %q", topic))
			}
			addUnique(topicOwners, applicationTopic, fmt.Sprintf("application topic for source %q", topic), "topic")
		}
		for topic := range pre.ApplicationTopics {
			if _, ok := sourceTopics[topic]; !ok {
				errs = append(errs, fmt.Errorf("preprovisioned applicationTopics contains unknown source %q", topic))
			}
		}
		sessionNames := make(map[string]string, len(pre.Sessions))
		for i, session := range pre.Sessions {
			if session.Name == "" || session.Group == "" {
				errs = append(errs, errors.New("preprovisioned session name and group must not be empty"))
			}
			addUnique(sessionNames, session.Name, fmt.Sprintf("session slot %d", i+1), "session name")
			addUnique(groupOwners, session.Group, fmt.Sprintf("session %q", session.Name), "group")
			for _, topic := range source.Topics {
				sessionTopic := session.Topics[topic]
				if sessionTopic == "" {
					errs = append(errs, fmt.Errorf("preprovisioned session %q missing topic for source %q", session.Name, topic))
				}
				addUnique(topicOwners, sessionTopic, fmt.Sprintf("session %q for source %q", session.Name, topic), "topic")
			}
			for topic := range session.Topics {
				if _, ok := sourceTopics[topic]; !ok {
					errs = append(errs, fmt.Errorf("preprovisioned session %q topics contains unknown source %q", session.Name, topic))
				}
			}
		}
	default:
		errs = append(errs, fmt.Errorf("invalid shadows.mode %q", shadows.Mode))
	}
	return errs
}

// Validate checks a KafkaRoute independently of its referenced split.
func (r *KafkaRoute) Validate() error {
	var errs []error
	if r.Spec.SplitRef.Name == "" {
		errs = append(errs, errors.New("splitRef.name must not be empty"))
	}
	if r.Spec.AttachmentID == "" || r.Spec.SessionID == "" {
		errs = append(errs, errors.New("attachmentID and sessionID must not be empty"))
	}
	if r.Spec.ExpiresAt.IsZero() {
		errs = append(errs, errors.New("expiresAt must not be zero"))
	}
	if r.Spec.DesiredState != RouteStateActive && r.Spec.DesiredState != RouteStateClosing {
		errs = append(errs, fmt.Errorf("invalid desiredState %q", r.Spec.DesiredState))
	}
	predicate := kafkaintercept.Predicate{
		Headers:   make(map[string][]byte, len(r.Spec.Predicate.Headers)),
		Key:       r.Spec.Predicate.Key,
		KeyPrefix: r.Spec.Predicate.KeyPrefix,
	}
	for _, header := range r.Spec.Predicate.Headers {
		if _, ok := predicate.Headers[header.Name]; ok {
			errs = append(errs, fmt.Errorf("duplicate predicate header %q", header.Name))
		}
		predicate.Headers[header.Name] = header.Value
	}
	if err := predicate.Validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
