package managerutil

import (
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// AgentsAreCompatible returns whether all the specified agents have the same
// product, version, and mechanisms. This might not be true for a number of
// reasons, such as when a deployment is restarting its Pods for an upgrade.
// This helper also compares Agent names as a sanity check.
func AgentsAreCompatible(agents []*rpc.AgentInfo) bool {
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
