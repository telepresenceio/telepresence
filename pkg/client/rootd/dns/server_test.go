package dns

import (
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
)

type suiteServer struct {
	suite.Suite

	server *Server
}

func (s *suiteServer) SetupSuite() {
	s.server = &Server{
		cache: xsync.NewMapOf[cacheKey, *cacheEntry](),
	}
}

func (s *suiteServer) TestSetMappings() {
	// given
	entry := &cacheEntry{wait: make(chan struct{}), created: time.Now()}
	aliasKeyA := cacheKey{name: "echo-easy-alias.", qType: dns.TypeA}
	aliasKeyAAAA := cacheKey{name: "echo-easy-alias.", qType: dns.TypeAAAA}
	aliasedToKeyA := cacheKey{name: "echo-easy.blue.svc.cluster.local.", qType: dns.TypeA}
	aliasedToKeyAAAA := cacheKey{name: "echo-easy.blue.svc.cluster.local.", qType: dns.TypeA}

	s.server.cache.Store(aliasKeyA, entry)
	s.server.cache.Store(aliasKeyAAAA, entry)
	s.server.cache.Store(aliasedToKeyA, entry)
	s.server.cache.Store(aliasedToKeyAAAA, entry)

	s.server.mappings = map[string]string{}

	// when
	s.server.SetMappings([]*rpc.DNSMapping{
		{
			Name:     "echo-easy-alias",
			AliasFor: "echo-easy.blue.svc.cluster.local",
		},
	})

	// then
	_, exists := s.server.cache.Load(aliasKeyA)
	s.False(exists, "Mapping's A record wasn't purged")
	_, exists = s.server.cache.Load(aliasKeyAAAA)
	s.False(exists, "Mapping's AAAA record wasn't purged")
	_, exists = s.server.cache.Load(aliasedToKeyA)
	s.True(exists, "Service's A record was purged")
	_, exists = s.server.cache.Load(aliasedToKeyAAAA)
	s.True(exists, "Service's AAAA record was purged")

	s.Equal(s.server.mappings, map[string]string{
		"echo-easy-alias.": "echo-easy.blue.svc.cluster.local.",
	})

	// given
	s.server.cache.Store(aliasKeyA, entry)
	s.server.cache.Store(aliasKeyAAAA, entry)

	// when
	s.server.SetMappings([]*rpc.DNSMapping{})

	// then
	// mappings are empty
	s.Empty(s.server.mappings)

	// nothing is purged when clearing the mappings because mappings never make it to the cache
	_, exists = s.server.cache.Load(aliasKeyA)
	s.True(exists, "Mapping's A record was purged")
	_, exists = s.server.cache.Load(aliasKeyAAAA)
	s.True(exists, "Mapping's AAAA record was purged")
	_, exists = s.server.cache.Load(aliasedToKeyA)
	s.True(exists, "Service's A record was purged")
	_, exists = s.server.cache.Load(aliasedToKeyAAAA)
	s.True(exists, "Service's AAAA record was purged")
}

func (s *suiteServer) TestSetExcludes() {
	// given
	entry := &cacheEntry{wait: make(chan struct{}), created: time.Now()}
	toDeleteARecordKey := cacheKey{name: "echo-easy.", qType: dns.TypeA}
	toDelete4ARecordKey := cacheKey{name: "echo-easy.", qType: dns.TypeAAAA}
	toDeleteNewARecordKey := cacheKey{name: "new-excluded.", qType: dns.TypeAAAA}

	s.server.cache.Store(toDeleteARecordKey, entry)
	s.server.cache.Store(toDelete4ARecordKey, entry)
	s.server.cache.Store(toDeleteNewARecordKey, entry)

	s.server.excludes = []string{"echo-easy"}

	// when
	newExcluded := []string{"new-excluded"}
	s.server.SetExcludes(newExcluded)

	// then
	_, exists := s.server.cache.Load(toDeleteARecordKey)
	assert.False(s.T(), exists, "Excluded A record was purged")
	_, exists = s.server.cache.Load(toDelete4ARecordKey)
	assert.False(s.T(), exists, "Excluded AAAA record was purged")
	_, exists = s.server.cache.Load(toDeleteNewARecordKey)
	assert.False(s.T(), exists, "New excluded record was purged")
	assert.Equal(s.T(), newExcluded, s.server.excludes)
}

func (s *suiteServer) TestIsExcluded() {
	// given
	s.server.excludes = []string{
		"echo-easy",
	}
	s.server.search = []string{
		tel2SubDomainDot + "cluster.local",
		"blue.svc.cluster.local",
	}

	// when & then
	assert.True(s.T(), s.server.isExcluded("echo-easy"))
	assert.True(s.T(), s.server.isExcluded("echo-easy.tel2-search.cluster.local"))
	assert.True(s.T(), s.server.isExcluded("echo-easy.blue.svc.cluster.local"))
	assert.False(s.T(), s.server.isExcluded("something-else"))
}

func TestServerTestSuite(t *testing.T) {
	suite.Run(t, new(suiteServer))
}
