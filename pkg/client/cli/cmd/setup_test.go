package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSetupCmdFlags verifies that the setup command only exposes the flags it
// actually uses: its own flags, --manager-namespace, and the Kubernetes flags
// needed to resolve a kubeconfig and talk to the cluster directly. It must
// not carry the daemon session's networking flags, since setup never starts
// a daemon session (see connectAndProbe, which talks to the cluster via
// k8s.DaemonKubeconfig/k8s.ConnectCluster directly).
func TestSetupCmdFlags(t *testing.T) {
	flags := setupCmd().Flags()

	for _, name := range []string{
		"output",
		"input",
		"apply",
		"non-interactive",
		"manager-namespace",
		"context",
		"kubeconfig",
		"namespace",
	} {
		assert.NotNilf(t, flags.Lookup(name), "expected flag %q to be present", name)
	}

	for _, name := range []string{
		"docker",
		"expose",
		"hostname",
		"name",
		"vnat",
		"proxy-via",
		"also-proxy",
		"never-proxy",
		"reroute-local",
		"reroute-remote",
		"allow-conflicting-subnets",
		"mapped-namespaces",
	} {
		assert.Nilf(t, flags.Lookup(name), "expected flag %q to be absent", name)
	}
}
