package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

// istioSuite verifies that a client can resolve and reach hosts that only the
// service mesh knows about (Istio ServiceEntry) when engaging a meshed workload.
// See issue #2717. The suite requires an Istio installation with DNS proxying
// enabled (ISTIO_META_DNS_CAPTURE) in the test cluster and skips when absent.
type istioSuite struct {
	itest.Suite
	itest.TrafficManager
	svc string
}

func (s *istioSuite) SuiteName() string {
	return "Istio"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &istioSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h, svc: "echo-mesh"}
	})
}

// istioDialSubnet is Istio's default auto-allocation range for ServiceEntry
// virtual IPs.
const istioDialSubnet = "240.240.0.0/16"

// serviceEntrySuffix is the DNS suffix of the ServiceEntry host. It must not be
// resolvable outside the mesh.
const serviceEntrySuffix = ".istio-se"

func (s *istioSuite) SetupSuite() {
	ctx := s.Context()
	if err := itest.Run(ctx, "kubectl", "get", "deploy", "istiod", "--namespace", "istio-system"); err != nil {
		s.T().Skip("Istio is not installed in this cluster")
	}
	mesh, err := itest.Output(ctx, "kubectl", "get", "configmap", "istio", "--namespace", "istio-system", "-o", "jsonpath={.data.mesh}")
	if err != nil || !strings.Contains(mesh, "ISTIO_META_DNS_CAPTURE") {
		s.T().Skip("Istio is not configured with DNS proxying (ISTIO_META_DNS_CAPTURE)")
	}
	s.Suite.SetupSuite()

	// The workload must be deployed with an Istio sidecar, and its ServiceEntry
	// must exist before the sidecars start so that its virtual IP is programmed.
	rq := s.Require()
	rq.NoError(itest.Run(ctx, "kubectl", "label", "namespace", s.AppNamespace(), "istio-injection=enabled"))
	rq.NoError(s.writeServiceEntry(ctx))
	itest.ApplyEchoService(ctx, s.svc, s.AppNamespace(), 80)

	// The traffic-agent must dial the ServiceEntry subnet through the mesh, and
	// the client must route the ServiceEntry suffix to cluster DNS.
	s.TelepresenceHelmInstallOK(ctx, true,
		"--set", fmt.Sprintf("agent.serviceMesh.dialSubnets={%s}", istioDialSubnet),
		"--set", fmt.Sprintf("client.dns.includeSuffixes={%s}", serviceEntrySuffix),
	)
}

func (s *istioSuite) TearDownSuite() {
	ctx := s.Context()
	s.RollbackTM(ctx)
	s.DeleteSvcAndWorkload(ctx, "deploy", s.svc)
	_ = itest.Run(ctx, "kubectl", "delete", "serviceentry", "--namespace", s.AppNamespace(), s.svc)
	_ = itest.Run(ctx, "kubectl", "label", "namespace", s.AppNamespace(), "istio-injection-")
}

func (s *istioSuite) writeServiceEntry(ctx context.Context) error {
	se := fmt.Sprintf(`apiVersion: networking.istio.io/v1
kind: ServiceEntry
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  hosts: ["%[1]s%[3]s"]
  location: MESH_INTERNAL
  ports:
    - number: 80
      name: http
      protocol: HTTP
  resolution: DNS
  endpoints:
    - address: %[1]s.%[2]s.svc.cluster.local
`, s.svc, s.AppNamespace(), serviceEntrySuffix)
	f := filepath.Join(s.T().TempDir(), "service-entry.yaml")
	if err := os.WriteFile(f, []byte(se), 0o644); err != nil {
		return err
	}
	return itest.Run(ctx, "kubectl", "apply", "-f", f)
}

// Test_ResolveAndDialServiceEntry engages the meshed workload with a proxy-via
// for the ServiceEntry virtual IP range, then verifies that the ServiceEntry
// host resolves on the workstation and that HTTP traffic to it is routed by the
// mesh to the backing service.
func (s *istioSuite) Test_ResolveAndDialServiceEntry() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--proxy-via", istioDialSubnet+"="+s.svc)
	defer itest.TelepresenceQuitOk(ctx)

	host := s.svc + serviceEntrySuffix
	s.Eventually(func() bool {
		out, err := itest.Output(ctx, "curl", "-s", "--max-time", "5", "http://"+host+"/")
		return err == nil && strings.Contains(out, "Host: "+host)
	}, 90*time.Second, 5*time.Second, "ServiceEntry host %s did not become reachable", host)
}
