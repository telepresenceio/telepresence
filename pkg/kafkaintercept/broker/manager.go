package broker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/kafkaconfig"
)

// Prepared is the complete application-shadow inventory for one split.
type Prepared struct {
	Names             Names
	SourceTopics      []api.KafkaTopicStatus
	ApplicationTopics map[string]string
	ApplicationGroup  string
	Resources         []api.KafkaResourceStatus
}

// Session is the complete broker inventory for one personal route.
type Session struct {
	Group     string
	Topics    map[string]string
	Resources []api.KafkaResourceStatus
}

// Manager performs idempotent broker operations for one connection profile.
type Manager struct {
	client *kgo.Client
	admin  *kadm.Client
	opts   []kgo.Opt
}

// Open resolves provider-only credentials and opens an administrative client.
func Open(
	ctx context.Context,
	reader client.Reader,
	namespace string,
	connection api.KafkaConnectionSpec,
) (*Manager, error) {
	opts, err := kafkaconfig.Options(ctx, reader, namespace, connection)
	if err != nil {
		return nil, err
	}
	opts = append(opts, kgo.ClientID("tp-kafka"))
	kafkaClient, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("create Kafka administrative client: %w", err)
	}
	if err := kafkaClient.Ping(ctx); err != nil {
		kafkaClient.Close()
		return nil, fmt.Errorf("connect to Kafka: %w", err)
	}
	return &Manager{client: kafkaClient, admin: kadm.NewClient(kafkaClient), opts: opts}, nil
}

// DrainSession transactionally returns all unconsumed session records to the
// application shadows and commits the session group's offsets atomically.
func (m *Manager) DrainSession(
	ctx context.Context,
	split *api.KafkaSplit,
	routeName, group string,
	sessionTopics, applicationTopics map[string]string,
) error {
	memberless, err := m.GroupMemberless(ctx, group)
	if err != nil {
		return err
	}
	if !memberless {
		return fmt.Errorf("kafka session group %s still has members", group)
	}
	inverse := make(map[string]string, len(sessionTopics))
	for source, sessionTopic := range sessionTopics {
		applicationTopic := applicationTopics[source]
		if applicationTopic == "" {
			return fmt.Errorf("kafka application shadow missing for source topic %s", source)
		}
		inverse[sessionTopic] = applicationTopic
	}
	names := ResourceNames(split, split.Spec.Source.Group)
	if err := names.ValidateSession(routeName, slices.Sorted(maps.Keys(sessionTopics)), split.Spec.Shadows.Mode == api.ShadowModeManaged, false); err != nil {
		return err
	}
	opts := slices.Clone(m.opts)
	opts = append(opts,
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(slices.Sorted(maps.Values(sessionTopics))...),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.TransactionalID(names.DrainTransactional(routeName)),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.Balancers(kgo.CooperativeStickyBalancer()),
	)
	session, err := kgo.NewGroupTransactSession(opts...)
	if err != nil {
		return fmt.Errorf("create Kafka session drain: %w", err)
	}
	defer session.Close()
	for {
		pollCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		fetches := session.PollRecords(pollCtx, 100)
		pollErr := pollCtx.Err()
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := fetches.Err(); err != nil && pollErr == nil {
			return fmt.Errorf("poll Kafka session residue: %w", err)
		}
		records := fetches.Records()
		if len(records) == 0 {
			remaining, err := m.Remaining(ctx, group, sessionTopics)
			if err != nil {
				return err
			}
			if remaining == 0 {
				return nil
			}
			continue
		}
		if err := session.Begin(); err != nil {
			return fmt.Errorf("begin Kafka session drain transaction: %w", err)
		}
		outputs := make([]*kgo.Record, len(records))
		for i, record := range records {
			destination := inverse[record.Topic]
			if destination == "" {
				_, _ = session.End(context.WithoutCancel(ctx), kgo.TryAbort)
				return fmt.Errorf("kafka session topic %s has no application destination", record.Topic)
			}
			outputs[i] = &kgo.Record{
				Topic: destination, Partition: record.Partition,
				Key: record.Key, Value: record.Value, Headers: record.Headers, Timestamp: record.Timestamp,
			}
		}
		if err := session.ProduceSync(ctx, outputs...).FirstErr(); err != nil {
			_, _ = session.End(context.WithoutCancel(ctx), kgo.TryAbort)
			return fmt.Errorf("return Kafka session residue: %w", err)
		}
		if _, err := session.End(ctx, kgo.TryCommit); err != nil {
			return fmt.Errorf("commit Kafka session drain: %w", err)
		}
	}
}

// Close releases broker connections.
func (m *Manager) Close() {
	m.client.Close()
}

// Prepare verifies source incarnations and creates or verifies application
// shadows.
func (m *Manager) Prepare(ctx context.Context, split *api.KafkaSplit) (Prepared, error) {
	names := ResourceNames(split, split.Spec.Source.Group)
	replicas := split.Spec.Splitter.Replicas
	if replicas <= 0 {
		replicas = 1
	}
	if err := names.ValidateApplication(
		split.Spec.Source.Topics, replicas, split.Spec.Shadows.Mode == api.ShadowModeManaged,
		split.Spec.Application.TransactionalIDEnv != "",
	); err != nil {
		return Prepared{}, err
	}
	details, err := m.admin.ListTopics(ctx, split.Spec.Source.Topics...)
	if err != nil {
		return Prepared{}, fmt.Errorf("describe Kafka source topics: %w", err)
	}
	result := Prepared{Names: names, ApplicationTopics: make(map[string]string, len(split.Spec.Source.Topics))}
	for _, source := range split.Spec.Source.Topics {
		detail, ok := details[source]
		if !ok || detail.Err != nil || len(detail.Partitions) == 0 {
			if ok && detail.Err != nil {
				return Prepared{}, fmt.Errorf("describe Kafka source topic %s: %w", source, detail.Err)
			}
			return Prepared{}, fmt.Errorf("kafka source topic %s does not exist", source)
		}
		result.SourceTopics = append(result.SourceTopics, api.KafkaTopicStatus{
			Name: source, TopicID: detail.ID.String(), Partitions: int32(len(detail.Partitions)),
		})
	}

	switch split.Spec.Shadows.Mode {
	case api.ShadowModeManaged:
		result.ApplicationGroup = names.ApplicationGroup
		for _, source := range split.Spec.Source.Topics {
			result.ApplicationTopics[source] = names.ApplicationTopic(source)
		}
	case api.ShadowModePreprovisioned:
		result.ApplicationGroup = split.Spec.Shadows.Preprovisioned.ApplicationGroup
		result.ApplicationTopics = maps.Clone(split.Spec.Shadows.Preprovisioned.ApplicationTopics)
	default:
		return Prepared{}, fmt.Errorf("unsupported Kafka shadow mode %q", split.Spec.Shadows.Mode)
	}

	for _, source := range split.Spec.Source.Topics {
		sourceDetail := details[source]
		destination := result.ApplicationTopics[source]
		shadow, err := m.ensureShadow(ctx, split, source, destination, sourceDetail)
		if err != nil {
			return Prepared{}, err
		}
		result.Resources = append(result.Resources, api.KafkaResourceStatus{
			Kind: "ApplicationTopic", Name: destination, TopicID: shadow.ID.String(), Source: source,
			Managed: split.Spec.Shadows.Mode == api.ShadowModeManaged,
		})
	}
	result.Resources = append(result.Resources, api.KafkaResourceStatus{
		Kind: "ApplicationGroup", Name: result.ApplicationGroup,
		Managed: split.Spec.Shadows.Mode == api.ShadowModeManaged,
	})
	return result, nil
}

// EnsureSession creates or claims all shadows for route.
func (m *Manager) EnsureSession(
	ctx context.Context,
	split *api.KafkaSplit,
	route *api.KafkaRoute,
) (Session, error) {
	names := ResourceNames(split, split.Spec.Source.Group)
	if err := names.ValidateSession(
		route.Name, split.Spec.Source.Topics, split.Spec.Shadows.Mode == api.ShadowModeManaged,
		split.Spec.Application.TransactionalIDEnv != "",
	); err != nil {
		return Session{}, err
	}
	result := Session{Topics: make(map[string]string, len(split.Spec.Source.Topics))}
	managed := split.Spec.Shadows.Mode == api.ShadowModeManaged
	if managed {
		result.Group = names.SessionGroup(route.Name)
		for _, source := range split.Spec.Source.Topics {
			result.Topics[source] = names.SessionTopic(route.Name, source)
		}
	} else {
		var slot *api.KafkaSessionSlot
		for i := range split.Spec.Shadows.Preprovisioned.Sessions {
			candidate := &split.Spec.Shadows.Preprovisioned.Sessions[i]
			if candidate.Name == route.Name || candidate.Group == route.Status.Group {
				slot = candidate
				break
			}
		}
		if slot == nil {
			return Session{}, fmt.Errorf("KafkaRoute %s has no preprovisioned session slot", route.Name)
		}
		result.Group = slot.Group
		result.Topics = maps.Clone(slot.Topics)
	}
	details, err := m.admin.ListTopics(ctx, split.Spec.Source.Topics...)
	if err != nil {
		return Session{}, fmt.Errorf("describe Kafka source topics: %w", err)
	}
	for _, source := range split.Spec.Source.Topics {
		detail, ok := details[source]
		if !ok || detail.Err != nil || len(detail.Partitions) == 0 {
			if ok && detail.Err != nil {
				return Session{}, fmt.Errorf("describe Kafka source topic %s: %w", source, detail.Err)
			}
			return Session{}, fmt.Errorf("kafka source topic %s does not exist", source)
		}
		shadow, err := m.ensureShadow(ctx, split, source, result.Topics[source], detail)
		if err != nil {
			return Session{}, err
		}
		result.Resources = append(result.Resources, api.KafkaResourceStatus{
			Kind: "SessionTopic", Name: result.Topics[source], TopicID: shadow.ID.String(), Source: source,
			Managed: managed, RouteName: route.Name,
		})
	}
	result.Resources = append(result.Resources, api.KafkaResourceStatus{
		Kind: "SessionGroup", Name: result.Group, Managed: managed, RouteName: route.Name,
	})
	return result, nil
}

func (m *Manager) ensureShadow(
	ctx context.Context,
	split *api.KafkaSplit,
	source, destination string,
	sourceDetail kadm.TopicDetail,
) (kadm.TopicDetail, error) {
	if destination == "" {
		return kadm.TopicDetail{}, fmt.Errorf("kafka shadow topic for %s is empty", source)
	}
	managed := split.Spec.Shadows.Mode == api.ShadowModeManaged
	details, err := m.admin.ListTopics(ctx, destination)
	if err != nil {
		return kadm.TopicDetail{}, fmt.Errorf("describe Kafka shadow topic %s: %w", destination, err)
	}
	detail, exists := details[destination]
	if !exists || errors.Is(detail.Err, kerr.UnknownTopicOrPartition) {
		if !managed {
			return kadm.TopicDetail{}, fmt.Errorf("preprovisioned Kafka shadow topic %s does not exist", destination)
		}
		if err := m.createShadow(ctx, split, destination, sourceDetail); err != nil {
			return kadm.TopicDetail{}, err
		}
		detail, err = m.waitForPartitions(ctx, destination, len(sourceDetail.Partitions))
		if err != nil {
			return kadm.TopicDetail{}, err
		}
	}
	if detail.Err != nil {
		return kadm.TopicDetail{}, fmt.Errorf("describe Kafka shadow topic %s: %w", destination, detail.Err)
	}
	wantPartitions := len(sourceDetail.Partitions)
	havePartitions := len(detail.Partitions)
	if havePartitions < wantPartitions {
		if !managed {
			return kadm.TopicDetail{}, fmt.Errorf("kafka shadow topic %s needs %d partitions, has %d", destination, wantPartitions, havePartitions)
		}
		responses, err := m.admin.UpdatePartitions(ctx, wantPartitions, destination)
		if err != nil {
			return kadm.TopicDetail{}, fmt.Errorf("expand Kafka shadow topic %s: %w", destination, err)
		}
		response, ok := responses[destination]
		if !ok || response.Err != nil {
			if !ok {
				return kadm.TopicDetail{}, fmt.Errorf("expand Kafka shadow topic %s: missing broker response", destination)
			}
			return kadm.TopicDetail{}, fmt.Errorf("expand Kafka shadow topic %s: %w", destination, response.Err)
		}
		detail, err = m.waitForPartitions(ctx, destination, wantPartitions)
		if err != nil {
			return kadm.TopicDetail{}, err
		}
	} else if havePartitions > wantPartitions {
		return kadm.TopicDetail{}, fmt.Errorf("kafka shadow topic %s has %d partitions, source %s has %d", destination, havePartitions, source, wantPartitions)
	}
	if err := m.verifyShadow(ctx, split, detail, sourceDetail); err != nil {
		return kadm.TopicDetail{}, err
	}
	return detail, nil
}

func (m *Manager) waitForPartitions(ctx context.Context, topic string, partitions int) (kadm.TopicDetail, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.client.ForceMetadataRefresh()
		details, err := m.admin.ListTopics(ctx, topic)
		if err == nil {
			if detail, ok := details[topic]; ok && detail.Err == nil && len(detail.Partitions) == partitions {
				return detail, nil
			}
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return kadm.TopicDetail{}, fmt.Errorf("describe created Kafka shadow topic %s: %w", topic, err)
			}
			return kadm.TopicDetail{}, fmt.Errorf("describe created Kafka shadow topic %s: %w", topic, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (m *Manager) createShadow(
	ctx context.Context,
	split *api.KafkaSplit,
	destination string,
	source kadm.TopicDetail,
) error {
	configs, replicationFactor, err := m.shadowConfigs(ctx, split, source)
	if err != nil {
		return err
	}
	responses, err := m.admin.CreateTopics(ctx, int32(len(source.Partitions)), replicationFactor, configs, destination)
	if err != nil {
		return fmt.Errorf("create Kafka shadow topic %s: %w", destination, err)
	}
	response, ok := responses[destination]
	if !ok {
		return fmt.Errorf("create Kafka shadow topic %s: missing broker response", destination)
	}
	if response.Err != nil && !errors.Is(response.Err, kerr.TopicAlreadyExists) {
		return fmt.Errorf("create Kafka shadow topic %s: %w", destination, response.Err)
	}
	return nil
}

func (m *Manager) shadowConfigs(
	ctx context.Context,
	split *api.KafkaSplit,
	source kadm.TopicDetail,
) (map[string]*string, int16, error) {
	sourceConfigs, err := m.topicConfig(ctx, source.Topic)
	if err != nil {
		return nil, 0, err
	}
	configured := split.Spec.Shadows.Managed
	configs := make(map[string]*string)
	if configured != nil {
		for key, value := range configured.Configs {
			configValue := value
			configs[key] = &configValue
		}
	}
	for key, value := range map[string]string{
		"cleanup.policy": "delete", "retention.ms": "-1", "retention.bytes": "-1",
	} {
		configValue := value
		configs[key] = &configValue
	}
	for _, key := range []string{"max.message.bytes", "min.insync.replicas", "message.timestamp.type"} {
		if value := sourceConfigs[key]; value != "" {
			configValue := value
			configs[key] = &configValue
		}
	}
	replicationFactor := int16(-1)
	for _, partition := range source.Partitions {
		if len(partition.Replicas) > int(replicationFactor) {
			replicationFactor = int16(len(partition.Replicas))
		}
	}
	if configured != nil && configured.ReplicationFactor != nil {
		if *configured.ReplicationFactor < int32(replicationFactor) {
			return nil, 0, fmt.Errorf(
				"configured shadow replication factor %d is weaker than source %s factor %d",
				*configured.ReplicationFactor, source.Topic, replicationFactor,
			)
		}
		replicationFactor = int16(*configured.ReplicationFactor)
	}
	return configs, replicationFactor, nil
}

func (m *Manager) verifyShadow(
	ctx context.Context,
	split *api.KafkaSplit,
	shadow, source kadm.TopicDetail,
) error {
	topic := shadow.Topic
	configs, err := m.topicConfig(ctx, topic)
	if err != nil {
		return err
	}
	if configs["retention.ms"] != "-1" || configs["retention.bytes"] != "-1" {
		return fmt.Errorf("kafka shadow topic %s must have effective unbounded retention", topic)
	}
	if configs["cleanup.policy"] != "delete" {
		return fmt.Errorf("kafka shadow topic %s must use cleanup.policy=delete", topic)
	}
	sourceConfigs, err := m.topicConfig(ctx, source.Topic)
	if err != nil {
		return err
	}
	shadowMax, _ := strconv.ParseInt(configs["max.message.bytes"], 10, 64)
	sourceMax, _ := strconv.ParseInt(sourceConfigs["max.message.bytes"], 10, 64)
	if shadowMax < sourceMax {
		return fmt.Errorf("kafka shadow topic %s max.message.bytes is weaker than source %s", topic, source.Topic)
	}
	shadowISR, _ := strconv.ParseInt(configs["min.insync.replicas"], 10, 64)
	sourceISR, _ := strconv.ParseInt(sourceConfigs["min.insync.replicas"], 10, 64)
	if shadowISR < sourceISR {
		return fmt.Errorf("kafka shadow topic %s min.insync.replicas is weaker than source %s", topic, source.Topic)
	}
	if configs["message.timestamp.type"] != sourceConfigs["message.timestamp.type"] {
		return fmt.Errorf("kafka shadow topic %s has a different message.timestamp.type than source %s", topic, source.Topic)
	}
	requiredFactor := int32(0)
	if managed := split.Spec.Shadows.Managed; managed != nil && managed.ReplicationFactor != nil {
		requiredFactor = *managed.ReplicationFactor
	}
	for partition, sourcePartition := range source.Partitions {
		factor := int32(len(sourcePartition.Replicas))
		if requiredFactor > factor {
			factor = requiredFactor
		}
		shadowPartition, ok := shadow.Partitions[partition]
		if !ok || int32(len(shadowPartition.Replicas)) < factor {
			return fmt.Errorf("kafka shadow topic %s partition %d has replication factor below %d", topic, partition, factor)
		}
	}
	return nil
}

func (m *Manager) topicConfig(ctx context.Context, topic string) (map[string]string, error) {
	resources, err := m.admin.DescribeTopicConfigs(ctx, topic)
	if err != nil {
		return nil, fmt.Errorf("describe Kafka topic config %s: %w", topic, err)
	}
	resource, err := resources.On(topic, func(resource *kadm.ResourceConfig) error { return resource.Err })
	if err != nil {
		return nil, fmt.Errorf("describe Kafka topic config %s: %w", topic, err)
	}
	configs := make(map[string]string, len(resource.Configs))
	for i := range resource.Configs {
		configs[resource.Configs[i].Key] = resource.Configs[i].MaybeValue()
	}
	return configs, nil
}

// GroupMemberless reports whether a classic consumer group has no live member.
func (m *Manager) GroupMemberless(ctx context.Context, group string) (bool, error) {
	members, err := m.GroupMembers(ctx, group)
	if err != nil {
		return false, err
	}
	return len(members) == 0, nil
}

// GroupMembers returns stable instance IDs, falling back to broker member IDs.
func (m *Manager) GroupMembers(ctx context.Context, group string) ([]string, error) {
	groups, err := m.admin.DescribeGroups(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("describe Kafka group %s: %w", group, err)
	}
	result, ok := groups[group]
	if !ok {
		return nil, fmt.Errorf("describe Kafka group %s: missing broker response", group)
	}
	if result.Err != nil {
		if errors.Is(result.Err, kerr.GroupIDNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("describe Kafka group %s: %w", group, result.Err)
	}
	members := make([]string, len(result.Members))
	for i := range result.Members {
		members[i] = result.Members[i].MemberID
		if result.Members[i].InstanceID != nil {
			members[i] = *result.Members[i].InstanceID
		}
	}
	slices.Sort(members)
	return members, nil
}

// Remaining returns the number of readable records after the group's
// committed positions. Transaction control markers are not records.
func (m *Manager) Remaining(ctx context.Context, group string, topics map[string]string) (int64, error) {
	destinations := slices.Sorted(maps.Values(topics))
	ends, err := m.admin.ListEndOffsets(ctx, destinations...)
	if err != nil {
		return 0, fmt.Errorf("list Kafka end offsets: %w", err)
	}
	committed, err := m.admin.FetchOffsets(ctx, group)
	if err != nil {
		return 0, fmt.Errorf("fetch Kafka offsets for group %s: %w", group, err)
	}
	details, err := m.admin.ListTopics(ctx, destinations...)
	if err != nil {
		return 0, fmt.Errorf("describe Kafka shadows: %w", err)
	}
	var remaining int64
	for _, topic := range destinations {
		detail, ok := details[topic]
		if !ok || detail.Err != nil {
			if ok {
				return 0, fmt.Errorf("describe Kafka shadow %s: %w", topic, detail.Err)
			}
			return 0, fmt.Errorf("describe Kafka shadow %s: missing broker response", topic)
		}
		for partition, end := range ends[topic] {
			if end.Err != nil {
				return 0, fmt.Errorf("list Kafka end offset %s[%d]: %w", topic, partition, end.Err)
			}
			at := int64(0)
			if offset, ok := committed.Lookup(topic, partition); ok && offset.Err == nil && offset.At >= 0 {
				at = offset.At
			}
			if end.Offset > at {
				partitionDetail, ok := detail.Partitions[partition]
				if !ok || partitionDetail.Err != nil || partitionDetail.Leader < 0 {
					if ok && partitionDetail.Err != nil {
						return 0, fmt.Errorf("describe Kafka shadow %s[%d]: %w", topic, partition, partitionDetail.Err)
					}
					return 0, fmt.Errorf("kafka shadow %s[%d] has no leader", topic, partition)
				}
				count, err := m.readableRecords(
					ctx, topic, [16]byte(detail.ID), partition, partitionDetail.Leader, at, end.Offset,
				)
				if err != nil {
					return 0, err
				}
				remaining += count
			}
		}
	}
	return remaining, nil
}

func (m *Manager) readableRecords(
	ctx context.Context,
	topic string,
	topicID [16]byte,
	partition int32,
	leader int32,
	at int64,
	end int64,
) (int64, error) {
	const fetchBytes = 4 << 20
	decompressor := kgo.DefaultDecompressor()
	var remaining int64
	for at < end {
		request := kmsg.NewPtrFetchRequest()
		request.SetVersion(12)
		request.MaxWaitMillis = 500
		request.MinBytes = 1
		request.MaxBytes = fetchBytes
		request.IsolationLevel = 1
		requestTopic := kmsg.NewFetchRequestTopic()
		requestTopic.Topic = topic
		requestTopic.TopicID = topicID
		requestPartition := kmsg.NewFetchRequestTopicPartition()
		requestPartition.Partition = partition
		requestPartition.FetchOffset = at
		requestPartition.PartitionMaxBytes = fetchBytes
		requestTopic.Partitions = append(requestTopic.Partitions, requestPartition)
		request.Topics = append(request.Topics, requestTopic)
		rawResponse, err := m.client.Broker(int(leader)).Request(ctx, request)
		if err != nil {
			return 0, fmt.Errorf("fetch Kafka shadow %s[%d]: %w", topic, partition, err)
		}
		response, ok := rawResponse.(*kmsg.FetchResponse)
		if !ok {
			return 0, fmt.Errorf("fetch Kafka shadow %s[%d]: unexpected response %T", topic, partition, rawResponse)
		}
		if err := kerr.ErrorForCode(response.ErrorCode); err != nil {
			return 0, fmt.Errorf("fetch Kafka shadow %s[%d]: %w", topic, partition, err)
		}
		var responsePartition *kmsg.FetchResponseTopicPartition
		for i := range response.Topics {
			for j := range response.Topics[i].Partitions {
				if response.Topics[i].Partitions[j].Partition == partition {
					responsePartition = &response.Topics[i].Partitions[j]
					break
				}
			}
			break
		}
		if responsePartition == nil {
			return 0, fmt.Errorf("fetch Kafka shadow %s[%d]: missing broker response", topic, partition)
		}
		processed, next := kgo.ProcessFetchPartition(kgo.ProcessFetchPartitionOpts{
			Offset: at, IsolationLevel: kgo.ReadCommitted(), Topic: topic, Partition: partition,
		}, responsePartition, decompressor, nil)
		if processed.Err != nil {
			return 0, fmt.Errorf("fetch Kafka shadow %s[%d]: %w", topic, partition, processed.Err)
		}
		for _, record := range processed.Records {
			if record.Offset < end {
				remaining++
			}
		}
		if next > at {
			at = next
			continue
		}
		if responsePartition.LastStableOffset >= end {
			break
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return remaining, nil
}

// DeleteManaged removes explicitly inventoried topics and groups. Missing
// resources are successful retries; ambiguous responses fail closed.
func (m *Manager) DeleteManaged(ctx context.Context, resources []api.KafkaResourceStatus) error {
	var topics, groups []string
	for _, resource := range resources {
		if !resource.Managed {
			continue
		}
		switch {
		case strings.HasSuffix(resource.Kind, "Topic"):
			topics = append(topics, resource.Name)
		case strings.HasSuffix(resource.Kind, "Group"):
			groups = append(groups, resource.Name)
		}
	}
	if len(topics) > 0 {
		details, err := m.admin.ListTopics(ctx, topics...)
		if err != nil {
			return fmt.Errorf("describe Kafka topics before cleanup: %w", err)
		}
		for _, resource := range resources {
			if !resource.Managed || !strings.HasSuffix(resource.Kind, "Topic") {
				continue
			}
			detail, ok := details[resource.Name]
			if !ok || errors.Is(detail.Err, kerr.UnknownTopicOrPartition) {
				continue
			}
			if detail.Err != nil {
				return fmt.Errorf("describe Kafka topic %s before cleanup: %w", resource.Name, detail.Err)
			}
			if resource.TopicID != "" && detail.ID.String() != resource.TopicID {
				return fmt.Errorf("kafka topic %s was recreated and will not be deleted", resource.Name)
			}
		}
		responses, err := m.admin.DeleteTopics(ctx, topics...)
		if err != nil {
			return fmt.Errorf("delete Kafka topics: %w", err)
		}
		for _, topic := range topics {
			response, ok := responses[topic]
			if !ok {
				return fmt.Errorf("delete Kafka topic %s: missing broker response", topic)
			}
			if response.Err != nil && !errors.Is(response.Err, kerr.UnknownTopicOrPartition) {
				return fmt.Errorf("delete Kafka topic %s: %w", topic, response.Err)
			}
		}
	}
	for _, group := range groups {
		response, err := m.admin.DeleteGroup(ctx, group)
		if err != nil && !errors.Is(err, kerr.GroupIDNotFound) {
			return fmt.Errorf("delete Kafka group %s: %w", group, err)
		}
		if response.Err != nil && !errors.Is(response.Err, kerr.GroupIDNotFound) {
			return fmt.Errorf("delete Kafka group %s: %w", group, response.Err)
		}
	}
	return nil
}
