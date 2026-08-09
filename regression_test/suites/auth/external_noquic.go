package auth

import (
	"net"
	"path/filepath"
	"strconv"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// externalEndpointSpecNoQuic is externalEndpointSpec without the QuicTunnel
// block, under its own spec Key.
func externalEndpointSpecNoQuic(secretName string) managers.Spec {
	v := managers.AuthEnforcing().Values
	v.ExternalEndpoint = managers.ExternalEndpoint{
		Enabled: true,
		Port:    externalEndpointContainerPort,
		Service: managers.ExternalEndpointService{Type: "NodePort", Port: 443},
		TLS:     managers.ExternalEndpointTLS{SecretName: secretName},
	}
	return managers.Spec{Key: "external-endpoint-noquic", Values: v}
}

// ExternalEndpointNoQuic proves that with no QUIC tunnel published, connect,
// discovery, outbound traffic, and log gathering still work over the TLS
// control connection alone, while an intercept attempt is refused clearly.
type ExternalEndpointNoQuic struct {
	rt.Suite
}

func init() {
	rt.Register(&ExternalEndpointNoQuic{}, rt.InArea("auth"), rt.NeedsManager(managers.Default), rt.WithLabels(rt.Slow))
}

func (s *ExternalEndpointNoQuic) Test_ExternalEndpointNoQuicUDPBlocked() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()

	nodeIP, nodePort, caPath := setupExternalEndpointSpec(t, ctx, r, externalEndpointSpecNoQuic)
	managerAddress := "tls://" + net.JoinHostPort(nodeIP, strconv.Itoa(nodePort))

	// telepresenceGrantWithLogsRules (gate.go): connections create,
	// attachments create/get, logs get -- no pods/portforward at all. The
	// external path must not need it.
	name := createGateIdentity(t, ctx, r, "rtest-external-noquic", telepresenceGrantWithLogsRules)
	// Attachment reviews happen in the WORKLOAD's namespace with the kind as
	// a subresource, so the identity also needs kind-qualified attachment
	// (and diagnostic) grants where the Echo Deployment lives.
	grantInNamespace(t, ctx, r, name, ns, appNamespaceAttachmentRules)
	tok := kubectlCreateToken(t, ctx, r, name)

	path, err := buildBlackholedExternalKubeconfig(rt.Env{Ctx: ctx, T: t, R: r}, name, tok, managerAddress, caPath)
	s.Require().NoError(err)
	tp := r.CLIWithEnv(map[string]string{"KUBECONFIG": path})

	wl := s.Workload(workloads.Echo("external-noquic"))

	freeDefaultConnection(t, ns)
	quitDefensively(t, r, ctx, "ExternalEndpointNoQuic:Test_ExternalEndpointNoQuicUDPBlocked")

	_, stderr, err := tp.Run(ctx, "connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace)
	s.Require().NoError(err, "connect over the external endpoint with no QUIC tunnel published: %s", stderr)

	var st cli.Status
	s.Require().NoError(tp.JSON(ctx, &st, "status", "--format", "json"))
	s.True(st.UserDaemon.Running, "user daemon should be running")
	s.NotEmpty(st.TrafficManager.Name, "status should report a connected traffic manager")

	var entries []cli.ListEntry
	s.Require().NoError(tp.JSON(ctx, &entries, "list", "--format", "json", "-n", ns))
	found := false
	for _, e := range entries {
		if e.Name == wl.Name && e.Namespace == ns {
			found = true
			break
		}
	}
	s.True(found, "list should show %s", wl.Name)

	// No intercept is active here: this proves manager-mediated outbound
	// traffic (the TCP tunnel fallback) works without QUIC, over the same
	// TLS control connection as everything else in this test.
	rt.RoutedToCluster(t, wl.ServiceURL())

	outFile := filepath.Join(t.TempDir(), "gather-logs.zip")
	_, stderr, err = tp.Run(ctx, "gather-logs", "--output-file", outFile, "--traffic-agents=None")
	s.Require().NoError(err, "gather-logs: %s", stderr)
	s.True(zipHasManagerLog(t, outFile), "gather-logs zip should contain a traffic-manager log; StreamLogs rides the TLS connection, not QUIC")

	ls := s.LocalEcho()
	// Plain output format: with --format json the CLI emits errors as JSON
	// on stdout, and this assertion wants the stderr error line.
	interceptOpts := append(rt.ToLocal(ls, "http")(), cli.MountFalse()()...)
	iArgs := append([]string{"intercept", wl.Name, "--namespace", ns}, interceptOpts...)
	_, stderr, err = tp.Run(ctx, iArgs...)
	s.Require().Error(err, "intercept should be refused without a QUIC tunnel published")
	s.Contains(stderr, "requires a channel to the traffic-agent")
	s.Contains(stderr, "quicTunnel")

	_, stderr, err = tp.Run(ctx, "quit", "-s")
	s.Require().NoError(err, "quit -s: %s", stderr)
}
