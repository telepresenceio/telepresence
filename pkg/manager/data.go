package manager

import (
	"fmt"

	"github.com/datawire/telepresence2/pkg/rpc"
)

// agentsAreCompatible returns whether all the specified agents have the same
// product, version, and mechanisms. This might not be true for a number of
// reasons, such as when a deployment is restarting its Pods for an upgrade.
// This helper also compares Agent names as a sanity check.
func agentsAreCompatible(agents []*rpc.AgentInfo) bool {
	if len(agents) == 0 {
		return false
	}

	golden := agents[0]
	for _, agent := range agents[1:] {
		names := golden.Name == agent.Name
		products := golden.Product == agent.Product
		versions := golden.Version == agent.Version
		mechanisms := mechanismsAreTheSame(golden.Mechanisms, agent.Mechanisms)

		if !(names && products && versions && mechanisms) {
			return false
		}
	}

	return true
}

// mechanismsAreTheSame returns whether both lists of mechanisms contain the
// same mechanisms (name, product, and version). As a sanity check, this helper
// verifies that the mechanism names in each list are distinct.
func mechanismsAreTheSame(a, b []*rpc.AgentInfo_Mechanism) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}

	goldenMap := make(map[string]*rpc.AgentInfo_Mechanism)
	for _, mechanism := range a {
		goldenMap[mechanism.Name] = mechanism
	}

	if len(goldenMap) != len(a) {
		// Names aren't unique
		return false
	}

	for _, mechanism := range b {
		golden, ok := goldenMap[mechanism.Name]
		if !ok {
			// b contains a name not present in a
			return false
		}

		product := golden.Product == mechanism.Product
		version := golden.Version == mechanism.Version
		if !(product && version) {
			return false
		}
	}

	return true
}

func agentHasMechanism(agent *rpc.AgentInfo, mechName string) bool {
	for _, mechanism := range agent.Mechanisms {
		if mechanism.Name == mechName {
			return true
		}
	}

	return false
}

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
	case agent.Hostname == "":
		return "hostname must not be empty"
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
	case spec.Name == "":
		return "name must not be empty"
	case spec.Client == "":
		return "client must not be empty"
	case spec.Agent == "":
		return "agent must not be empty"
	case spec.Mechanism == "":
		return "mechanism must not be empty"
	}

	return ""
}
