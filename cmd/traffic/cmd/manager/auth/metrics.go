package auth

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics groups the counters published for the external listener's
// authentication path. TokenReview is the expensive, API-server-bound step,
// so the counters separate what avoided it (CacheHits) from what triggered it
// (FirstReviews, FallbackReviews) and from the admission controls that reject
// a call before either happens (RateLimited).
type Metrics struct {
	// CacheHits counts Authenticate calls resolved from the token-review
	// cache, without a new TokenReview call.
	CacheHits prometheus.Counter
	// FirstReviews counts TokenReview calls made with the manager audience.
	FirstReviews prometheus.Counter
	// FallbackReviews counts the deliberate no-audience TokenReview retry,
	// which serves tokens minted for the API server only.
	FallbackReviews prometheus.Counter
	// RateLimited counts calls rejected by the external listener's admission
	// controls (concurrency cap, QPS/burst limiter, or oversized metadata)
	// before either TokenReview could run.
	RateLimited prometheus.Counter
	// InvalidTokens counts bearer tokens the API server rejected outright.
	InvalidTokens prometheus.Counter
	// APIFailures counts TokenReview calls that failed for infrastructure
	// reasons rather than rejecting the token.
	APIFailures prometheus.Counter
}

// authMetricVecs are the process-wide counter vectors backing NewMetrics,
// one series per listener label value, registered once.
var authMetricVecs = struct { //nolint:gochecknoglobals // prometheus vectors registered once at load

	cacheHits       *prometheus.CounterVec
	firstReviews    *prometheus.CounterVec
	fallbackReviews *prometheus.CounterVec
	rateLimited     *prometheus.CounterVec
	invalidTokens   *prometheus.CounterVec
	apiFailures     *prometheus.CounterVec
}{
	cacheHits:       authMetricVec("telepresence_auth_cache_hits", "Bearer token authentications resolved from cache"),
	firstReviews:    authMetricVec("telepresence_auth_first_reviews", "Manager-audience TokenReview calls made"),
	fallbackReviews: authMetricVec("telepresence_auth_fallback_reviews", "No-audience fallback TokenReview calls made"),
	rateLimited:     authMetricVec("telepresence_auth_rate_limited", "TokenReview attempts rejected by review admission"),
	invalidTokens:   authMetricVec("telepresence_auth_invalid_tokens", "Bearer tokens the API server rejected"),
	apiFailures:     authMetricVec("telepresence_auth_api_failures", "TokenReview calls that failed for infrastructure reasons"),
}

func authMetricVec(name, help string) *prometheus.CounterVec {
	return promauto.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, []string{"listener"})
}

// NewMetrics returns the authentication metrics for the named listener
// ("external" or "internal"). Each listener labels its own series of the
// shared counter vectors, so it is safe to call once per listener.
func NewMetrics(listener string) *Metrics {
	return &Metrics{
		CacheHits:       authMetricVecs.cacheHits.WithLabelValues(listener),
		FirstReviews:    authMetricVecs.firstReviews.WithLabelValues(listener),
		FallbackReviews: authMetricVecs.fallbackReviews.WithLabelValues(listener),
		RateLimited:     authMetricVecs.rateLimited.WithLabelValues(listener),
		InvalidTokens:   authMetricVecs.invalidTokens.WithLabelValues(listener),
		APIFailures:     authMetricVecs.apiFailures.WithLabelValues(listener),
	}
}

// unregisteredMetrics returns a *Metrics of plain, unregistered counters,
// unlike NewMetrics which registers them with the default registry.
func unregisteredMetrics() *Metrics {
	c := func() prometheus.Counter { return prometheus.NewCounter(prometheus.CounterOpts{Name: "c"}) }
	return &Metrics{
		CacheHits:       c(),
		FirstReviews:    c(),
		FallbackReviews: c(),
		RateLimited:     c(),
		InvalidTokens:   c(),
		APIFailures:     c(),
	}
}
