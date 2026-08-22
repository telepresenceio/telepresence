package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	validation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

const kafkaAPIPath = "/apis/kafka.telepresence.io/v1alpha1"

type kafkaAPI struct {
	rest       rest.Interface
	refreshMu  sync.Mutex
	refreshed  map[string]time.Time
	expiration time.Duration
}

type kafkaObjectMeta struct {
	Name              string     `json:"name,omitempty"`
	Namespace         string     `json:"namespace,omitempty"`
	Generation        int64      `json:"generation,omitempty"`
	DeletionTimestamp *time.Time `json:"deletionTimestamp,omitempty"`
}

type kafkaWorkload struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type kafkaSplitResource struct {
	Metadata kafkaObjectMeta `json:"metadata"`
	Status   struct {
		ActiveGeneration int64           `json:"activeGeneration"`
		Phase            string          `json:"phase"`
		Workloads        []kafkaWorkload `json:"workloads"`
	} `json:"status"`
}

type kafkaSplitList struct {
	Items []kafkaSplitResource `json:"items"`
}

type kafkaRoutePredicate struct {
	Headers   []kafkaHeader `json:"headers,omitempty"`
	Key       []byte        `json:"key,omitempty"`
	KeyPrefix []byte        `json:"keyPrefix,omitempty"`
}

type kafkaHeader struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

type kafkaRouteSpec struct {
	SplitRef struct {
		Name string `json:"name"`
	} `json:"splitRef"`
	AttachmentID string              `json:"attachmentID"`
	SessionID    string              `json:"sessionID"`
	ExpiresAt    time.Time           `json:"expiresAt"`
	DesiredState string              `json:"desiredState"`
	Predicate    kafkaRoutePredicate `json:"predicate,omitempty"`
}

type kafkaRouteResource struct {
	APIVersion string          `json:"apiVersion,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Metadata   kafkaObjectMeta `json:"metadata"`
	Spec       kafkaRouteSpec  `json:"spec"`
	Status     struct {
		Phase       string            `json:"phase"`
		Environment map[string]string `json:"environment,omitempty"`
		Conditions  []struct {
			Message string `json:"message"`
		} `json:"conditions,omitempty"`
	} `json:"status,omitempty"`
}

func newKafkaAPI(client rest.Interface, clientTTL time.Duration) *kafkaAPI {
	if clientTTL < time.Minute {
		clientTTL = time.Minute
	}
	return &kafkaAPI{rest: client, refreshed: make(map[string]time.Time), expiration: clientTTL}
}

func (k *kafkaAPI) attach(
	ctx context.Context,
	namespace, workload, workloadKind, clientName, interceptName, attachmentID, sessionID string,
	request *rpc.KafkaIntercept,
) ([]*rpc.KafkaRoute, map[string]string, error) {
	splits, err := k.matchingSplits(ctx, namespace, workload, workloadKind)
	if err != nil {
		return nil, nil, err
	}
	if len(splits) == 0 {
		if request.GetOnly() {
			return nil, nil, fmt.Errorf("workload %s.%s has no enabled KafkaSplit", workload, namespace)
		}
		return nil, nil, nil
	}

	created := make([]kafkaRouteResource, 0, len(splits))
	rollback := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var errs []error
		for i := range created {
			if err := k.delete(cleanupCtx, namespace, created[i].Metadata.Name); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	for i := range splits {
		desired, err := desiredKafkaRoute(
			namespace, splits[i].Metadata.Name, clientName, interceptName,
			attachmentID, sessionID, request, k.expiresAt(),
		)
		if err != nil {
			return nil, nil, errors.Join(err, rollback())
		}
		route, err := k.create(ctx, desired)
		if err != nil {
			return nil, nil, errors.Join(err, rollback())
		}
		created = append(created, route)
	}

	summaries := make([]*rpc.KafkaRoute, 0, len(created))
	environment := make(map[string]string)
	for i := range created {
		route, err := k.waitReady(ctx, namespace, created[i].Metadata.Name)
		if err != nil {
			return nil, nil, errors.Join(err, rollback())
		}
		for name, value := range route.Status.Environment {
			if previous, ok := environment[name]; ok && previous != value {
				return nil, nil, errors.Join(
					fmt.Errorf("kafka routes provide conflicting values for %s", name),
					rollback(),
				)
			}
			environment[name] = value
		}
		summaries = append(summaries, &rpc.KafkaRoute{
			Name: route.Metadata.Name, Split: route.Spec.SplitRef.Name,
			Environment: maps.Clone(route.Status.Environment),
		})
	}
	slices.SortFunc(summaries, func(a, b *rpc.KafkaRoute) int { return compare(a.GetSplit(), b.GetSplit()) })
	return summaries, environment, nil
}

func (k *kafkaAPI) matchingSplits(
	ctx context.Context,
	namespace, workload, workloadKind string,
) ([]kafkaSplitResource, error) {
	bytes, err := k.rest.Get().AbsPath(kafkaCollection(namespace, "splits")).Do(ctx).Raw()
	if err != nil {
		return nil, fmt.Errorf("list KafkaSplits: %w", err)
	}
	var list kafkaSplitList
	if err := json.Unmarshal(bytes, &list); err != nil {
		return nil, fmt.Errorf("decode KafkaSplits: %w", err)
	}
	list.Items = slices.DeleteFunc(list.Items, func(split kafkaSplitResource) bool {
		if split.Metadata.DeletionTimestamp != nil ||
			(split.Status.Phase != "Enabled" && split.Status.Phase != "Starting") ||
			split.Status.ActiveGeneration == 0 || split.Status.ActiveGeneration != split.Metadata.Generation {
			return true
		}
		return !slices.ContainsFunc(split.Status.Workloads, func(candidate kafkaWorkload) bool {
			return candidate.Name == workload && (workloadKind == "" || candidate.Kind == workloadKind)
		})
	})
	slices.SortFunc(list.Items, func(a, b kafkaSplitResource) int { return compare(a.Metadata.Name, b.Metadata.Name) })
	return list.Items, nil
}

func desiredKafkaRoute(
	namespace, split, clientName, interceptName, attachmentID, sessionID string,
	request *rpc.KafkaIntercept,
	expiresAt time.Time,
) (kafkaRouteResource, error) {
	name, err := kafkaRouteName(clientName, interceptName, split)
	if err != nil {
		return kafkaRouteResource{}, err
	}
	route := kafkaRouteResource{
		APIVersion: "kafka.telepresence.io/v1alpha1", Kind: "KafkaRoute",
		Metadata: kafkaObjectMeta{Namespace: namespace, Name: name},
		Spec: kafkaRouteSpec{
			AttachmentID: attachmentID, SessionID: sessionID, ExpiresAt: expiresAt, DesiredState: "Active",
			Predicate: kafkaRoutePredicate{Key: slices.Clone(request.GetKey()), KeyPrefix: slices.Clone(request.GetKeyPrefix())},
		},
	}
	route.Spec.SplitRef.Name = split
	for _, header := range request.GetHeaders() {
		route.Spec.Predicate.Headers = append(route.Spec.Predicate.Headers, kafkaHeader{
			Name: header.GetName(), Value: slices.Clone(header.GetValue()),
		})
	}
	return route, nil
}

func (k *kafkaAPI) create(ctx context.Context, desired kafkaRouteResource) (kafkaRouteResource, error) {
	bytes, err := json.Marshal(desired)
	if err != nil {
		return kafkaRouteResource{}, err
	}
	result, err := k.rest.Post().AbsPath(kafkaCollection(desired.Metadata.Namespace, "routes")).
		SetHeader("Content-Type", "application/json").Body(bytes).Do(ctx).Raw()
	if err == nil {
		var created kafkaRouteResource
		if err := json.Unmarshal(result, &created); err != nil {
			return kafkaRouteResource{}, fmt.Errorf("decode created KafkaRoute: %w", err)
		}
		return created, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return kafkaRouteResource{}, fmt.Errorf("create KafkaRoute %s: %w", desired.Metadata.Name, err)
	}
	existing, err := k.get(ctx, desired.Metadata.Namespace, desired.Metadata.Name)
	if err != nil {
		return kafkaRouteResource{}, err
	}
	if existing.Metadata.DeletionTimestamp != nil || existing.Spec.DesiredState != "Active" ||
		existing.Spec.SplitRef.Name != desired.Spec.SplitRef.Name ||
		existing.Spec.AttachmentID != desired.Spec.AttachmentID || existing.Spec.SessionID != desired.Spec.SessionID ||
		!reflect.DeepEqual(existing.Spec.Predicate, desired.Spec.Predicate) {
		return kafkaRouteResource{}, fmt.Errorf(
			"KafkaRoute %s already exists with a different owner or predicate; "+
				"choose client, intercept, or KafkaSplit names that normalize differently",
			desired.Metadata.Name,
		)
	}
	if err := k.patchExpiry(ctx, desired.Metadata.Namespace, desired.Metadata.Name, desired.Spec.ExpiresAt); err != nil {
		return kafkaRouteResource{}, err
	}
	return existing, nil
}

func (k *kafkaAPI) waitReady(ctx context.Context, namespace, name string) (kafkaRouteResource, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		route, err := k.get(ctx, namespace, name)
		if err != nil {
			return kafkaRouteResource{}, err
		}
		switch route.Status.Phase {
		case "Ready":
			return route, nil
		case "Invalid", "Closed":
			message := route.Status.Phase
			if n := len(route.Status.Conditions); n > 0 && route.Status.Conditions[n-1].Message != "" {
				message = route.Status.Conditions[n-1].Message
			}
			return kafkaRouteResource{}, fmt.Errorf("KafkaRoute %s is %s: %s", name, route.Status.Phase, message)
		}
		select {
		case <-ctx.Done():
			return kafkaRouteResource{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (k *kafkaAPI) get(ctx context.Context, namespace, name string) (kafkaRouteResource, error) {
	bytes, err := k.rest.Get().AbsPath(kafkaResource(namespace, "routes", name)).Do(ctx).Raw()
	if err != nil {
		return kafkaRouteResource{}, fmt.Errorf("get KafkaRoute %s: %w", name, err)
	}
	var route kafkaRouteResource
	if err := json.Unmarshal(bytes, &route); err != nil {
		return kafkaRouteResource{}, fmt.Errorf("decode KafkaRoute %s: %w", name, err)
	}
	return route, nil
}

func (k *kafkaAPI) close(ctx context.Context, namespace string, routes []*rpc.KafkaRoute) error {
	var errs []error
	for _, route := range routes {
		if err := k.delete(ctx, namespace, route.GetName()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (k *kafkaAPI) delete(ctx context.Context, namespace, name string) error {
	_, err := k.rest.Delete().AbsPath(kafkaResource(namespace, "routes", name)).Do(ctx).Raw()
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete KafkaRoute %s: %w", name, err)
	}
	return nil
}

func (k *kafkaAPI) refresh(ctx context.Context, namespace string, routes []*rpc.KafkaRoute) error {
	now := time.Now()
	expiresAt := k.expiresAt()
	var errs []error
	for _, route := range routes {
		key := namespace + "/" + route.GetName()
		k.refreshMu.Lock()
		last := k.refreshed[key]
		if now.Sub(last) < k.expiration {
			k.refreshMu.Unlock()
			continue
		}
		k.refreshed[key] = now
		k.refreshMu.Unlock()
		if err := k.patchExpiry(ctx, namespace, route.GetName(), expiresAt); err != nil {
			errs = append(errs, err)
			k.refreshMu.Lock()
			delete(k.refreshed, key)
			k.refreshMu.Unlock()
		}
	}
	return errors.Join(errs...)
}

func (k *kafkaAPI) patchExpiry(ctx context.Context, namespace, name string, expiresAt time.Time) error {
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"expiresAt": expiresAt}})
	if err != nil {
		return err
	}
	_, err = k.rest.Patch(types.MergePatchType).AbsPath(kafkaResource(namespace, "routes", name)).Body(patch).Do(ctx).Raw()
	if err != nil {
		return fmt.Errorf("refresh KafkaRoute %s: %w", name, err)
	}
	return nil
}

func (k *kafkaAPI) expiresAt() time.Time {
	return time.Now().Add(3 * k.expiration).UTC()
}

func kafkaCollection(namespace, resource string) string {
	return kafkaAPIPath + "/namespaces/" + url.PathEscape(namespace) + "/" + resource
}

func kafkaResource(namespace, resource, name string) string {
	return kafkaCollection(namespace, resource) + "/" + url.PathEscape(name)
}

func kafkaRouteName(clientName, interceptName, split string) (string, error) {
	parts := make([]string, 3)
	for i, value := range []struct {
		kind string
		name string
	}{{"client", clientName}, {"intercept", interceptName}, {"KafkaSplit", split}} {
		parts[i] = kafkaRouteSegment(value.name)
		if parts[i] == "" {
			return "", fmt.Errorf("%s name %q has no letters or digits for a KafkaRoute name", value.kind, value.name)
		}
	}
	name := strings.Join(parts, "-")
	if len(name) > validation.DNS1123SubdomainMaxLength {
		return "", fmt.Errorf(
			"KafkaRoute name derived from the client, intercept, and KafkaSplit is %d characters; shorten the visible names to fit the %d-character limit",
			len(name), validation.DNS1123SubdomainMaxLength,
		)
	}
	if messages := validation.IsDNS1123Subdomain(name); len(messages) > 0 {
		return "", fmt.Errorf(
			"KafkaRoute name derived from client %q, intercept %q, and KafkaSplit %q is invalid: %s",
			clientName, interceptName, split, strings.Join(messages, ", "),
		)
	}
	return name, nil
}

func kafkaRouteSegment(name string) string {
	var result strings.Builder
	separator := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if separator && result.Len() > 0 {
				result.WriteByte('-')
			}
			result.WriteRune(r)
			separator = false
		} else {
			separator = true
		}
	}
	return result.String()
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
