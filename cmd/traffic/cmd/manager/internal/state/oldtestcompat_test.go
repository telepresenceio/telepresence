package state

import (
	"time"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func (s *State) IsPresent(sessionID string) bool {
	_, err := s.SessionDone(sessionID)
	return err == nil
}

func (s *State) HasClient(sessionID string) bool {
	return s.GetClient(sessionID) != nil
}

func (s *State) Add(sessionID, clientName string, now time.Time) string {
	return s.addClient(sessionID, &rpc.ClientInfo{Name: clientName}, now)
}

type ClientInfo rpc.ClientInfo

func (c *ClientInfo) Item() interface{} {
	return c.Name
}

func (s *State) Get(sessionID string) *ClientInfo {
	return (*ClientInfo)(s.GetClient(sessionID))
}

func (s *State) Mark(sessionID string, now time.Time) bool {
	req := &rpc.RemainRequest{
		Session: &rpc.SessionInfo{
			SessionId: sessionID,
		},
	}
	return s.MarkSession(req, now)
}
