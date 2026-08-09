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

// NewMetrics registers and returns the external listener's authentication
// metrics. Call at most once per process -- a second call panics on duplicate
// registration with the default Prometheus registry -- which is why the
// caller only constructs one when the external listener is actually enabled.
func NewMetrics() *Metrics {
	c := func(name, help string) prometheus.Counter {
		return promauto.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	}
	return &Metrics{
		CacheHits:       c("telepresence_external_auth_cache_hits", "Bearer token authentications resolved from cache on the external listener"),
		FirstReviews:    c("telepresence_external_auth_first_reviews", "Manager-audience TokenReview calls made by the external listener"),
		FallbackReviews: c("telepresence_external_auth_fallback_reviews", "No-audience fallback TokenReview calls made by the external listener"),
		RateLimited:     c("telepresence_external_auth_rate_limited", "Requests the external listener rejected before TokenReview could run"),
		InvalidTokens:   c("telepresence_external_auth_invalid_tokens", "Bearer tokens the API server rejected on the external listener"),
		APIFailures:     c("telepresence_external_auth_api_failures", "TokenReview calls that failed for infrastructure reasons on the external listener"),
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
