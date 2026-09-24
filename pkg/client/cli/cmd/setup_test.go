package cmd

import (
	"testing"

	"github.com/spf13/cobra"
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
		"namespace",
	} {
		assert.Nilf(t, flags.Lookup(name), "expected flag %q to be absent", name)
	}
}

// TestSetupCmdRequiresOutputOrApply verifies that run rejects a call with
// neither --output nor --apply before it touches the cluster.
func TestSetupCmdRequiresOutputOrApply(t *testing.T) {
	sc := &setupCommand{}
	err := sc.run(&cobra.Command{}, nil)
	assert.ErrorContains(t, err,
		"specify --output <file> (or --output -) to write the values, --apply to install them, or both")
}
