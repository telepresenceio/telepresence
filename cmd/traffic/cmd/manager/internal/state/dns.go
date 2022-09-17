package state

import (
	"context"
	"sync"
	"time"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

// AgentsLookup will send the given request to all agents currently intercepted by the client identified with
// the clientSessionID, it will then wait for results to arrive, collect those results, and return them as a
// unique and sorted slice together with a count of how many agents that replied.
func (s *State) AgentsLookup(ctx context.Context, clientSessionID string, request *rpc.LookupHostRequest) (iputil.IPs, int, error) {
	iceptAgentIDs := s.getAgentsInterceptedByClient(clientSessionID)
	ips := iputil.IPs{}
	iceptCount := len(iceptAgentIDs)
	if iceptCount == 0 {
		return ips, 0, nil
	}

	rsMu := sync.Mutex{} // prevent concurrent updates of the ips slice
	agentTimeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	count := 0
	wg := sync.WaitGroup{}
	wg.Add(iceptCount)
	for _, agentSessionID := range iceptAgentIDs {
		go func(agentSessionID string) {
			defer func() {
				s.endHostLookup(agentSessionID, request)
				wg.Done()
			}()

			rsCh := s.startHostLookup(agentSessionID, request)
			if rsCh == nil {
				return
			}
			select {
			case <-agentTimeout.Done():
				return
			case rs := <-rsCh:
				if rs == nil {
					// Channel closed
					return
				}
				rsMu.Lock()
				count++
				for _, ip := range rs.Ips {
					ips = append(ips, ip)
				}
				rsMu.Unlock()
			}
		}(agentSessionID)
	}
	wg.Wait() // wait for timeout or that all agents have responded
	return ips.UniqueSorted(), count, nil
}

// PostLookupResponse receives lookup responses from an agent and places them in the channel
// that corresponds to the lookup request
func (s *State) PostLookupResponse(response *rpc.LookupHostAgentResponse) {
	responseID := response.Request.Session.SessionId + ":" + response.Request.Host
	var rch chan<- *rpc.LookupHostResponse
	s.mu.RLock()
	if as, ok := s.sessions[response.Session.SessionId].(*agentSessionState); ok {
		rch = as.lookupResponses[responseID]
	}
	s.mu.RUnlock()
	if rch != nil {
		rch <- response.Response
	}
}

func (s *State) startHostLookup(agentSessionID string, request *rpc.LookupHostRequest) <-chan *rpc.LookupHostResponse {
	responseID := request.Session.SessionId + ":" + request.Host
	var (
		rch chan *rpc.LookupHostResponse
		as  *agentSessionState
		ok  bool
	)
	s.mu.Lock()
	if as, ok = s.sessions[agentSessionID].(*agentSessionState); ok {
		if rch, ok = as.lookupResponses[responseID]; !ok {
			rch = make(chan *rpc.LookupHostResponse)
			as.lookupResponses[responseID] = rch
		}
	}
	s.mu.Unlock()
	if as != nil {
		// the as.lookups channel may be closed at this point, so guard for panic
		func() {
			defer func() {
				if r := recover(); r != nil {
					close(rch)
				}
			}()
			as.lookups <- request
		}()
	}
	return rch
}

func (s *State) endHostLookup(agentSessionID string, request *rpc.LookupHostRequest) {
	responseID := request.Session.SessionId + ":" + request.Host
	s.mu.Lock()
	if as, ok := s.sessions[agentSessionID].(*agentSessionState); ok {
		if rch, ok := as.lookupResponses[responseID]; ok {
			delete(as.lookupResponses, responseID)
			close(rch)
		}
	}
	s.mu.Unlock()
}

func (s *State) WatchLookupHost(agentSessionID string) <-chan *rpc.LookupHostRequest {
	s.mu.RLock()
	ss, ok := s.sessions[agentSessionID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return ss.(*agentSessionState).lookups
}
