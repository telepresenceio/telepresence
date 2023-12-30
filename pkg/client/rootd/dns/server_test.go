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
	toDeleteARecordKey := cacheKey{name: "echo-easy-alias.", qType: dns.TypeA}
	toDelete4ARecordKey := cacheKey{name: "echo-easy-alias.", qType: dns.TypeAAAA}
	toKeepARecordKey := cacheKey{name: "echo-easy.blue.svc.cluster.local.", qType: dns.TypeA}

	s.server.cache.Store(toDeleteARecordKey, entry)
	s.server.cache.Store(toDelete4ARecordKey, entry)
	s.server.cache.Store(toKeepARecordKey, entry)

	s.server.mappings = map[string]string{
		"echo-easy-alias.": "echo-easy.blue.svc.cluster.local.",
	}

	// when
	s.server.SetMappings([]*rpc.DNSMapping{})

	// then
	_, exists := s.server.cache.Load(toDeleteARecordKey)
	s.False(exists, "Mapping's A record was purged")
	_, exists = s.server.cache.Load(toDelete4ARecordKey)
	s.False(exists, "Mapping's AAAA record was purged")
	_, exists = s.server.cache.Load(toKeepARecordKey)
	s.Truef(exists, "Service's A record wasn't purged")
	s.Empty(s.server.mappings)
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
		tel2SubDomainDot + "cluster.local.",
		"blue.svc.cluster.local.",
	}

	// when & then
	assert.True(s.T(), s.server.isExcluded("echo-easy."))
	assert.True(s.T(), s.server.isExcluded("echo-easy.tel2-search.cluster.local."))
	assert.True(s.T(), s.server.isExcluded("echo-easy.blue.svc.cluster.local."))
	assert.False(s.T(), s.server.isExcluded("something-else."))
}

func TestServerTestSuite(t *testing.T) {
	suite.Run(t, new(suiteServer))
}
