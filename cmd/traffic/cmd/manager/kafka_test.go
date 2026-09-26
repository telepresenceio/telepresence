package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
)

func TestKafkaAttachmentLifecycle(t *testing.T) {
	var mutex sync.Mutex
	var route api.KafkaRoute
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == kafkaCollection("shop", "splits"):
			split := api.KafkaSplit{ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "shop", Generation: 4}}
			split.Status.ActiveGeneration = 4
			split.Status.Phase = api.SplitPhaseStarting
			split.Status.Workloads = []api.WorkloadReference{{Kind: "Deployment", Name: "checkout"}}
			_ = json.NewEncoder(w).Encode(api.KafkaSplitList{Items: []api.KafkaSplit{split}})
		case request.Method == http.MethodPost:
			require.NoError(t, json.NewDecoder(request.Body).Decode(&route))
			route.Status.Phase = api.RoutePhaseStaged
			_ = json.NewEncoder(w).Encode(route)
		case request.Method == http.MethodGet && route.Name != "":
			route.Status.Phase = api.RoutePhaseReady
			route.Status.Environment = map[string]string{"ORDERS_TOPIC": "tp.orders.route", "KAFKA_GROUP": "tp.orders.group"}
			_ = json.NewEncoder(w).Encode(route)
		case request.Method == http.MethodPatch:
			_ = json.NewEncoder(w).Encode(route)
		case request.Method == http.MethodDelete:
			deleted = true
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "Success"})
		default:
			http.Error(w, request.Method+" "+request.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	kafka := newKafkaAPI(testRESTClient(t, server), time.Minute)
	routes, environment, err := kafka.attach(
		t.Context(), "shop", "checkout", "Deployment", "Alice@Laptop", "My Orders", "session:intercept", "session",
		&rpc.KafkaIntercept{Headers: []*rpc.KafkaHeader{{Name: "tenant", Value: []byte("blue")}}},
	)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	require.Equal(t, "alice-laptop-my-orders-orders", routes[0].Name)
	require.Equal(t, "orders", routes[0].Split)
	require.Equal(t, "tp.orders.route", environment["ORDERS_TOPIC"])
	require.Equal(t, "tenant", route.Spec.Predicate.Headers[0].Name)

	// The refresh cache gains an entry once the route is created and read
	// back; close() must forget it.
	require.NoError(t, kafka.refresh(t.Context(), "shop", routes))
	kafka.refreshMu.Lock()
	_, cached := kafka.refreshed["shop/"+routes[0].Name]
	kafka.refreshMu.Unlock()
	require.True(t, cached, "refresh must have recorded the route")

	require.NoError(t, kafka.close(t.Context(), "shop", routes))
	require.True(t, deleted)

	kafka.refreshMu.Lock()
	_, stillCached := kafka.refreshed["shop/"+routes[0].Name]
	kafka.refreshMu.Unlock()
	require.False(t, stillCached, "close must forget the route's refresh cache entry")
}

func TestKafkaOnlyRequiresMatchingSplit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.KafkaSplitList{})
	}))
	defer server.Close()
	kafka := newKafkaAPI(testRESTClient(t, server), time.Minute)
	_, _, err := kafka.attach(t.Context(), "shop", "checkout", "Deployment", "alice", "orders", "attachment", "session", &rpc.KafkaIntercept{Only: true})
	require.ErrorContains(t, err, "no enabled KafkaSplit")
}

func TestKafkaAttachmentRollsBackEveryCreatedRoute(t *testing.T) {
	var mutex sync.Mutex
	routes := make(map[string]api.KafkaRoute)
	deleted := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == kafkaCollection("shop", "splits"):
			list := api.KafkaSplitList{}
			for _, name := range []string{"orders", "payments"} {
				split := api.KafkaSplit{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Generation: 1}}
				split.Status.ActiveGeneration = 1
				split.Status.Phase = api.SplitPhaseEnabled
				split.Status.Workloads = []api.WorkloadReference{{Kind: "Deployment", Name: "checkout"}}
				list.Items = append(list.Items, split)
			}
			_ = json.NewEncoder(w).Encode(list)
		case request.Method == http.MethodPost:
			var route api.KafkaRoute
			require.NoError(t, json.NewDecoder(request.Body).Decode(&route))
			route.Status.Phase = api.RoutePhaseStaged
			routes[route.Name] = route
			_ = json.NewEncoder(w).Encode(route)
		case request.Method == http.MethodGet:
			name := request.URL.Path[strings.LastIndexByte(request.URL.Path, '/')+1:]
			route := routes[name]
			if route.Spec.SplitRef.Name == "orders" {
				route.Status.Phase = api.RoutePhaseReady
				route.Status.Environment = map[string]string{"ORDERS_TOPIC": "orders-alice"}
			} else {
				route.Status.Phase = api.RoutePhaseInvalid
				route.Status.Conditions = append(route.Status.Conditions, metav1.Condition{Message: "no preprovisioned session available"})
			}
			_ = json.NewEncoder(w).Encode(route)
		case request.Method == http.MethodDelete:
			name := request.URL.Path[strings.LastIndexByte(request.URL.Path, '/')+1:]
			deleted[name] = true
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "Success"})
		default:
			http.Error(w, request.Method+" "+request.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	kafka := newKafkaAPI(testRESTClient(t, server), time.Minute)
	_, _, err := kafka.attach(t.Context(), "shop", "checkout", "Deployment", "alice", "checkout", "attachment", "session", &rpc.KafkaIntercept{})
	require.ErrorContains(t, err, "no preprovisioned session available")
	require.Len(t, routes, 2)
	require.Len(t, deleted, 2)
	for name := range routes {
		require.True(t, deleted[name], name)
	}
}

// TestKafkaMatchingSplitsNotFound_Degrades: a 404 listing KafkaSplits (the
// CRD is not installed) is treated as "no Kafka routes" for an ordinary
// intercept, but still fails a Kafka-only intercept.
func TestKafkaMatchingSplitsNotFound_Degrades(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(metav1.Status{
			Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
			Message: "the server could not find the requested resource",
		})
	}))
	defer server.Close()
	kafka := newKafkaAPI(testRESTClient(t, server), time.Minute)

	routes, environment, err := kafka.attach(t.Context(), "shop", "checkout", "Deployment", "alice", "orders", "attachment", "session", &rpc.KafkaIntercept{})
	require.NoError(t, err)
	require.Empty(t, routes)
	require.Empty(t, environment)

	_, _, err = kafka.attach(t.Context(), "shop", "checkout", "Deployment", "alice", "orders", "attachment", "session", &rpc.KafkaIntercept{Only: true})
	require.Error(t, err)
	require.ErrorContains(t, err, "KafkaSplit resources are unavailable")
}

// TestKafkaMatchingSplitsForbidden_Degrades mirrors the NotFound case for a
// ServiceAccount that cannot list KafkaSplits.
func TestKafkaMatchingSplitsForbidden_Degrades(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(metav1.Status{
			Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden,
			Message: "splits.kafka.telepresence.io is forbidden",
		})
	}))
	defer server.Close()
	kafka := newKafkaAPI(testRESTClient(t, server), time.Minute)

	routes, environment, err := kafka.attach(t.Context(), "shop", "checkout", "Deployment", "alice", "orders", "attachment", "session", &rpc.KafkaIntercept{})
	require.NoError(t, err)
	require.Empty(t, routes)
	require.Empty(t, environment)

	_, _, err = kafka.attach(t.Context(), "shop", "checkout", "Deployment", "alice", "orders", "attachment", "session", &rpc.KafkaIntercept{Only: true})
	require.Error(t, err)
	require.ErrorContains(t, err, "KafkaSplit resources are unavailable")

	// A predicate also keeps the error visible, even without Only.
	_, _, err = kafka.attach(t.Context(), "shop", "checkout", "Deployment", "alice", "orders", "attachment", "session", &rpc.KafkaIntercept{Key: []byte("k")})
	require.Error(t, err)
	require.ErrorContains(t, err, "KafkaSplit resources are unavailable")
}

func TestKafkaRouteName(t *testing.T) {
	tests := map[string]struct {
		client, intercept, split string
		want                     string
		wantError                string
	}{
		"visible": {
			client: "Alice@Laptop", intercept: "Orders Preview", split: "checkout-v2",
			want: "alice-laptop-orders-preview-checkout-v2",
		},
		"separators": {
			client: "--Alice..Laptop--", intercept: "my_intercept", split: "orders",
			want: "alice-laptop-my-intercept-orders",
		},
		"empty segment": {
			client: "@", intercept: "orders", split: "checkout",
			wantError: "client name",
		},
		"too long": {
			client: strings.Repeat("a", 210), intercept: strings.Repeat("b", 40), split: "checkout",
			wantError: "shorten the visible names",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := kafkaRouteName(test.client, test.intercept, test.split)
			if test.wantError == "" {
				require.NoError(t, err)
				require.Equal(t, test.want, got)
			} else {
				require.ErrorContains(t, err, test.wantError)
			}
		})
	}
	first, err := kafkaRouteName("Alice@Laptop", "orders", "checkout")
	require.NoError(t, err)
	second, err := kafkaRouteName("alice laptop", "orders", "checkout")
	require.NoError(t, err)
	require.Equal(t, first, second, "normalization collisions must remain visible to create conflict handling")
}

func testRESTClient(t *testing.T, server *httptest.Server) rest.Interface {
	t.Helper()
	config := &rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{
		NegotiatedSerializer: serializer.NewCodecFactory(runtime.NewScheme()).WithoutConversion(),
	}}
	client, err := rest.UnversionedRESTClientForConfigAndClient(config, server.Client())
	require.NoError(t, err)
	return client
}
