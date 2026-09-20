package auth

import (
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
)

// sarCacheCapacity bounds the verdict cache; a full cache evicts the entry
// closest to expiry.
const sarCacheCapacity = 4096

// sarCache memoizes SubjectAccessReview verdicts per principal and attribute
// set: an allowed verdict for successCacheTTL, a denied one for
// failureCacheTTL. Review errors are never stored.
type sarCache struct {
	mu      sync.Mutex
	entries map[string]sarEntry
}

type sarEntry struct {
	allowed bool
	expiry  time.Time
}

func newSARCache() *sarCache {
	return &sarCache{entries: make(map[string]sarEntry)}
}

func (c *sarCache) get(key string) (allowed, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return false, false
	}
	if time.Now().After(e.expiry) {
		delete(c.entries, key)
		return false, false
	}
	return e.allowed, true
}

func (c *sarCache) put(key string, allowed bool) {
	ttl := failureCacheTTL
	if allowed {
		ttl = successCacheTTL
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= sarCacheCapacity {
		evictKey := ""
		evictExpiry := now.Add(time.Hour)
		for k, e := range c.entries {
			if now.After(e.expiry) {
				delete(c.entries, k)
				continue
			}
			if e.expiry.Before(evictExpiry) {
				evictKey, evictExpiry = k, e.expiry
			}
		}
		if len(c.entries) >= sarCacheCapacity && evictKey != "" {
			delete(c.entries, evictKey)
		}
	}
	c.entries[key] = sarEntry{allowed: allowed, expiry: now.Add(ttl)}
}

// sarKey identifies one review: the principal's full identity (groups and
// extra claims influence the verdict) plus the attribute set.
func sarKey(p *Principal, ra *authorizationv1.ResourceAttributes) string {
	var b strings.Builder
	b.WriteString(p.Username)
	b.WriteByte(0)
	b.WriteString(p.UID)
	groups := slices.Clone(p.Groups)
	sort.Strings(groups)
	for _, g := range groups {
		b.WriteByte(0)
		b.WriteString(g)
	}
	extraKeys := make([]string, 0, len(p.Extra))
	for k := range p.Extra {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		b.WriteByte(0)
		b.WriteString(k)
		for _, v := range p.Extra[k] {
			b.WriteByte(1)
			b.WriteString(v)
		}
	}
	for _, s := range []string{ra.Namespace, ra.Verb, ra.Group, ra.Resource, ra.Subresource, ra.Name} {
		b.WriteByte(0)
		b.WriteString(s)
	}
	return b.String()
}
