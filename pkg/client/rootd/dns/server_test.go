package dns

import (
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
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
		cache: sync.Map{},
		config: &rpc.DNSConfig{
			Excludes: nil,
			Mappings: nil,
		},
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

	s.server.config.Mappings = []*rpc.DNSMapping{
		{
			Name:     "echo-easy-alias",
			AliasFor: "echo-easy.blue.svc.cluster.local",
		},
	}

	// when
	s.server.SetMappings([]*rpc.DNSMapping{})

	// then
	_, exists := s.server.cache.Load(toDeleteARecordKey)
	assert.False(s.T(), exists, "Mapping's A record was purged")
	_, exists = s.server.cache.Load(toDelete4ARecordKey)
	assert.False(s.T(), exists, "Mapping's AAAA record was purged")
	_, exists = s.server.cache.Load(toKeepARecordKey)
	assert.Truef(s.T(), exists, "Service's A record wasn't purged")
	assert.Equal(s.T(), []*rpc.DNSMapping{}, s.server.config.Mappings)
}

func TestServerTestSuite(t *testing.T) {
	suite.Run(t, new(suiteServer))
}
