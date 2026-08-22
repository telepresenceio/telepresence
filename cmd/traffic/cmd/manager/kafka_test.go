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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestKafkaAttachmentLifecycle(t *testing.T) {
	var mutex sync.Mutex
	var route kafkaRouteResource
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == kafkaCollection("shop", "splits"):
			split := kafkaSplitResource{Metadata: kafkaObjectMeta{Name: "orders", Namespace: "shop", Generation: 4}}
			split.Status.ActiveGeneration = 4
			split.Status.Phase = "Starting"
			split.Status.Workloads = []kafkaWorkload{{Kind: "Deployment", Name: "checkout"}}
			_ = json.NewEncoder(w).Encode(kafkaSplitList{Items: []kafkaSplitResource{split}})
		case request.Method == http.MethodPost:
			require.NoError(t, json.NewDecoder(request.Body).Decode(&route))
			route.Status.Phase = "Staged"
			_ = json.NewEncoder(w).Encode(route)
		case request.Method == http.MethodGet && route.Metadata.Name != "":
			route.Status.Phase = "Ready"
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
	require.NoError(t, kafka.close(t.Context(), "shop", routes))
	require.True(t, deleted)
}

func TestKafkaOnlyRequiresMatchingSplit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(kafkaSplitList{})
	}))
	defer server.Close()
	kafka := newKafkaAPI(testRESTClient(t, server), time.Minute)
	_, _, err := kafka.attach(t.Context(), "shop", "checkout", "Deployment", "alice", "orders", "attachment", "session", &rpc.KafkaIntercept{Only: true})
	require.ErrorContains(t, err, "no enabled KafkaSplit")
}

func TestKafkaAttachmentRollsBackEveryCreatedRoute(t *testing.T) {
	var mutex sync.Mutex
	routes := make(map[string]kafkaRouteResource)
	deleted := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == kafkaCollection("shop", "splits"):
			list := kafkaSplitList{}
			for _, name := range []string{"orders", "payments"} {
				split := kafkaSplitResource{Metadata: kafkaObjectMeta{Name: name, Namespace: "shop", Generation: 1}}
				split.Status.ActiveGeneration = 1
				split.Status.Phase = "Enabled"
				split.Status.Workloads = []kafkaWorkload{{Kind: "Deployment", Name: "checkout"}}
				list.Items = append(list.Items, split)
			}
			_ = json.NewEncoder(w).Encode(list)
		case request.Method == http.MethodPost:
			var route kafkaRouteResource
			require.NoError(t, json.NewDecoder(request.Body).Decode(&route))
			route.Status.Phase = "Staged"
			routes[route.Metadata.Name] = route
			_ = json.NewEncoder(w).Encode(route)
		case request.Method == http.MethodGet:
			name := request.URL.Path[strings.LastIndexByte(request.URL.Path, '/')+1:]
			route := routes[name]
			if route.Spec.SplitRef.Name == "orders" {
				route.Status.Phase = "Ready"
				route.Status.Environment = map[string]string{"ORDERS_TOPIC": "orders-alice"}
			} else {
				route.Status.Phase = "Invalid"
				route.Status.Conditions = append(route.Status.Conditions, struct {
					Message string `json:"message"`
				}{Message: "no preprovisioned session available"})
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
