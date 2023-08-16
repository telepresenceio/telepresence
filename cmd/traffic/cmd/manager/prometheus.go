package manager

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/utils/strings/slices"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
)

var PrometheusConsumptionCounterLabelsResolver = map[string]LabelResolver{
	"session_id": func(_ state.State, sessionID string) string {
		return sessionID
	},
} //nolint:gochecknoglobals // extension point

type sessionCounters struct {
	connectionDuration float64
	fromClientBytes    float64
	toClientBytes      float64
}

type LabelResolver func(state.State, string) string

func newConsumptionGauges() *consumptionGauges {
	labels := make([]string, 0)
	for label := range PrometheusConsumptionCounterLabelsResolver {
		labels = append(labels, label)
	}

	return &consumptionGauges{
		values: make(map[string]*sessionCounters),
		connectionDurationCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "consumption_connection_duration",
				Help: "Duration of the connection in seconds",
			},
			labels,
		),
		fromClientBytesCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "consumption_from_client_bytes",
				Help: "Amount of data sent from the client to the cluster",
			},
			labels,
		),
		toClientBytesCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "consumption_to_client_bytes",
				Help: "Amount of data sent from the cluster to the client",
			},
			labels,
		),
		labelResolvers: PrometheusConsumptionCounterLabelsResolver,
	}
}

type consumptionGauges struct {
	values map[string]*sessionCounters

	connectionDurationCounter *prometheus.CounterVec
	fromClientBytesCounter    *prometheus.CounterVec
	toClientBytesCounter      *prometheus.CounterVec

	labelResolvers map[string]LabelResolver
}

func (c *consumptionGauges) Init() {
	prometheus.MustRegister(c.connectionDurationCounter)
	prometheus.MustRegister(c.fromClientBytesCounter)
	prometheus.MustRegister(c.toClientBytesCounter)
}

func (c *consumptionGauges) resolveLabels(state state.State, sessionID string) prometheus.Labels {
	pLabels := prometheus.Labels{}

	for label, resolver := range c.labelResolvers {
		pLabels[label] = resolver(state, sessionID)
	}

	return pLabels
}

func (c *consumptionGauges) Update(state state.State) {
	consumptionMetrics := state.GetAllSessionConsumptionMetrics()
	for sessionID, consumptionMetric := range consumptionMetrics {
		if _, ok := c.values[sessionID]; !ok {
			c.values[sessionID] = &sessionCounters{}
		}

		pLabels := c.resolveLabels(state, sessionID)

		c.connectionDurationCounter.With(pLabels).Add(
			float64(consumptionMetric.ConnectDuration) - c.values[sessionID].connectionDuration)
		c.fromClientBytesCounter.With(pLabels).Add(
			float64(consumptionMetric.FromClientBytes.GetValue()) - c.values[sessionID].fromClientBytes)
		c.toClientBytesCounter.With(pLabels).Add(
			float64(consumptionMetric.ToClientBytes.GetValue()) - c.values[sessionID].toClientBytes)

		c.values[sessionID].connectionDuration = float64(consumptionMetric.ConnectDuration)
		c.values[sessionID].fromClientBytes = float64(consumptionMetric.FromClientBytes.GetValue())
		c.values[sessionID].toClientBytes = float64(consumptionMetric.ToClientBytes.GetValue())
	}

	activeSessions := make([]string, 0)
	for sessionID := range consumptionMetrics {
		activeSessions = append(activeSessions, sessionID)
	}

	c.cleanup(state)
}

func (c *consumptionGauges) cleanup(state state.State) {
	consumptionMetrics := state.GetAllSessionConsumptionMetrics()

	activeSessions := make([]string, 0)
	for sessionID := range consumptionMetrics {
		activeSessions = append(activeSessions, sessionID)
	}

	for sessionID := range c.values {
		if !slices.Contains(activeSessions, sessionID) {
			delete(c.values, sessionID)

			pLabels := c.resolveLabels(state, sessionID)

			c.connectionDurationCounter.Delete(pLabels)
			c.fromClientBytesCounter.Delete(pLabels)
			c.toClientBytesCounter.Delete(pLabels)
		}
	}
}

// ServePrometheus serves Prometheus metrics if env.PrometheusPort != 0.
func (s *service) servePrometheus(ctx context.Context) error {
	env := managerutil.GetEnv(ctx)
	if env.PrometheusPort == 0 {
		dlog.Info(ctx, "Prometheus metrics server not started")
		return nil
	}
	newGaugeFunc := func(n, h string, f func() int) {
		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Name: n,
			Help: h,
		}, func() float64 { return float64(f()) })
	}
	newGaugeFunc("agent_count", "Number of connected traffic agents", s.state.CountAgents)
	newGaugeFunc("client_count", "Number of connected clients", s.state.CountClients)
	newGaugeFunc("intercept_count", "Number of active intercepts", s.state.CountIntercepts)
	newGaugeFunc("session_count", "Number of sessions", s.state.CountSessions)
	newGaugeFunc("tunnel_count", "Number of tunnels", s.state.CountTunnels)

	newGaugeFunc("active_http_request_count", "Number of currently served http requests", func() int {
		return int(atomic.LoadInt32(&s.activeHttpRequests))
	})

	newGaugeFunc("active_grpc_request_count", "Number of currently served gRPC requests", func() int {
		return int(atomic.LoadInt32(&s.activeGrpcRequests))
	})

	wg := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		SoftShutdownTimeout: time.Second * 10,
		HardShutdownTimeout: time.Second * 10,
	})

	wg.Go("consumption-metrics", func(ctx context.Context) error {
		ticker := time.NewTicker(time.Second * 5)

		cg := newConsumptionGauges()
		cg.Init()
		for {
			select {
			case <-ticker.C:
				cg.Update(s.state)
			case <-ctx.Done():
				return nil
			}
		}
	})

	sc := &dhttp.ServerConfig{
		Handler: promhttp.Handler(),
	}
	dlog.Infof(ctx, "Prometheus metrics server started on port: %d", env.PrometheusPort)
	defer dlog.Info(ctx, "Prometheus metrics server stopped")
	return sc.ListenAndServe(ctx, fmt.Sprintf("%s:%d", env.ServerHost, env.PrometheusPort))
}
