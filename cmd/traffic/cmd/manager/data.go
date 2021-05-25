package manager

import (
	"fmt"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func validateClient(client *rpc.ClientInfo) string {
	switch {
	case client.Name == "":
		return "name must not be empty"
	case client.InstallId == "":
		return "install ID must not be empty"
	case client.Product == "":
		return "product must not be empty"
	case client.Version == "":
		return "version must not be empty"
	}

	return ""
}

func validateMechanism(mechanism *rpc.AgentInfo_Mechanism) string {
	switch {
	case mechanism.Name == "":
		return "name must not be empty"
	case mechanism.Product == "":
		return "product must not be empty"
	case mechanism.Version == "":
		return "version must not be empty"
	}

	return ""
}

func validateAgent(agent *rpc.AgentInfo) string {
	switch {
	case agent.Name == "":
		return "name must not be empty"
	case agent.Namespace == "":
		return "namespace must not be empty"
	case agent.Product == "":
		return "product must not be empty"
	case agent.Version == "":
		return "version must not be empty"
	case len(agent.Mechanisms) == 0:
		return "mechanisms must not be empty"
	}

	for idx, mechanism := range agent.Mechanisms {
		if msg := validateMechanism(mechanism); msg != "" {
			return fmt.Sprintf("mechanism %d: %s", idx+1, msg)
		}
	}

	return ""
}

func validateIntercept(spec *rpc.InterceptSpec) string {
	switch {
	case spec.Client == "":
		return "client must not be empty"
	case spec.Agent == "":
		return "agent must not be empty"
	case spec.Namespace == "":
		return "namespace must not be empty"
	case spec.Mechanism == "":
		return "mechanism must not be empty"
	}

	return ""
}
