package agent

import (
	"fmt"

	"github.com/datawire/telepresence2/pkg/rpc/manager"
)

// State of the Traffic Agent.
type State struct {
	forwarder   *Forwarder
	managerHost string
	appHost     string
	appPort     int32
	chosenID    string
}

func NewState(forwarder *Forwarder, managerHost string) *State {
	host, port := forwarder.Target()
	return &State{
		forwarder:   forwarder,
		managerHost: managerHost,
		appHost:     host,
		appPort:     port,
	}
}

func (s *State) HandleIntercepts(cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest {
	var chosenIntercept, activeIntercept *manager.InterceptInfo

	// Find the chosen intercept if it still exists
	if s.chosenID != "" {
		for _, cept := range cepts {
			if cept.Id == s.chosenID {
				chosenIntercept = cept
				break
			}
		}
	}

	if chosenIntercept != nil {
		// The chosen intercept still exists
		if chosenIntercept.Disposition == manager.InterceptDispositionType_ACTIVE {
			// and is active
			activeIntercept = chosenIntercept
		}
	} else {
		// The chosen intercept was deleted by the user
		s.chosenID = ""
	}

	// Update forwarding
	if activeIntercept != nil {
		s.forwarder.SetTarget(s.managerHost, activeIntercept.ManagerPort)
	} else {
		s.forwarder.SetTarget(s.appHost, s.appPort)
	}

	// Review waiting intercepts
	reviews := []*manager.ReviewInterceptRequest{}
	for _, cept := range cepts {
		if cept.Disposition == manager.InterceptDispositionType_WAITING {
			// This intercept is ready to be active
			if s.chosenID == "" {
				// We don't have an intercept in play, so choose this one. All
				// agents will get intercepts in the same order every time, so
				// this will yield a consistent result. Note that the intercept
				// will not become active at this time. That will happen later,
				// once the manager assigns a port.
				s.chosenID = cept.Id
				reviews = append(reviews, &manager.ReviewInterceptRequest{
					Id:          cept.Id,
					Disposition: manager.InterceptDispositionType_ACTIVE,
				})
			} else {
				// We already have an intercept in play, so reject this one.
				msg := fmt.Sprintf("Waiting to serve intercept %s", s.chosenID)
				if chosenIntercept != nil {
					msg = fmt.Sprintf(
						"Serving intercept %s from %s (%s)",
						chosenIntercept.Spec.Name,
						chosenIntercept.Spec.Client,
						s.chosenID,
					)
				}
				reviews = append(reviews, &manager.ReviewInterceptRequest{
					Id:          cept.Id,
					Disposition: manager.InterceptDispositionType_AGENT_ERROR,
					Message:     msg,
				})
			}
		}
	}

	return reviews
}
