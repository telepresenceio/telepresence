package state

import (
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func agentHasMechanism(agent *rpc.AgentInfo, mechName string) bool {
	for _, mechanism := range agent.Mechanisms {
		if mechanism.Name == mechName {
			return true
		}
	}

	return false
}
