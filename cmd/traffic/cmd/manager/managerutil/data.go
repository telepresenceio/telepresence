package managerutil

import (
	"strings"

	"github.com/blang/semver/v4"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// AgentsAreCompatible returns whether all the specified agents have the same
// product, release, and mechanisms. Builds of the same semantic-version core
// remain compatible while a workload rolls from one build to another; major,
// minor, and patch changes do not. This helper also compares Agent names as a
// sanity check.
func AgentsAreCompatible(agents []*rpc.AgentInfo) bool {
	if len(agents) == 0 {
		return false
	}

	golden := agents[0]
	for _, agent := range agents[1:] {
		names := golden.Name == agent.Name
		products := golden.Product == agent.Product
		versions := versionsAreCompatible(golden.Version, agent.Version)
		mechanisms := mechanismsAreTheSame(golden.Mechanisms, agent.Mechanisms)

		if !(names && products && versions && mechanisms) {
			return false
		}
	}

	return true
}

// mechanismsAreTheSame returns whether both lists contain mechanisms with the
// same names and products and compatible versions. As a sanity check, this
// helper verifies that the mechanism names in each list are distinct.
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
		version := versionsAreCompatible(golden.Version, mechanism.Version)
		if !(product && version) {
			return false
		}
	}

	return true
}

// versionsAreCompatible allows prerelease and build identifiers to differ
// within the same semantic-version release. Exact equality continues to
// support historical or otherwise non-semantic version strings.
func versionsAreCompatible(a, b string) bool {
	if a == b {
		return true
	}

	av, err := semver.Parse(strings.TrimPrefix(a, "v"))
	if err != nil {
		return false
	}
	bv, err := semver.Parse(strings.TrimPrefix(b, "v"))
	if err != nil {
		return false
	}
	return av.Major == bv.Major && av.Minor == bv.Minor && av.Patch == bv.Patch
}
