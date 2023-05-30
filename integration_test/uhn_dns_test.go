package integration

import (
	"fmt"
	"net"
	"time"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type unqualifiedHostNameDNSSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *unqualifiedHostNameDNSSuite) SuiteName() string {
	return "UnqualifiedHostNameDNS"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &unqualifiedHostNameDNSSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *unqualifiedHostNameDNSSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *unqualifiedHostNameDNSSuite) Test_UHNExcludes() {
	// given
	ctx := s.Context()
	serviceName := "echo"
	port := 80
	itest.ApplyEchoService(ctx, serviceName, s.AppNamespace(), port)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", serviceName)

	excludes := []string{
		"echo-easy",
		"echo-easy.blue",
		"echo-easy.blue.svc.cluster.local",
	}
	ctx = itest.WithKubeConfigExtension(ctx, func(cluster *api.Cluster) map[string]any {
		return map[string]any{"dns": map[string][]string{
			"excludes": excludes,
		}}
	})

	// when
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace(), "--context", "extra")

	// then
	for _, excluded := range excludes {
		s.Eventually(func() bool {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", excluded, port), 5000*time.Millisecond)
			if err == nil {
				_ = conn.Close()
			}
			return err != nil
		}, 10*time.Second, 1*time.Second, "should not be able to reach %s", excluded)
	}
}

func (s *unqualifiedHostNameDNSSuite) Test_UHNMappings() {
	// given
	ctx := s.Context()
	serviceName := "echo"
	port := 80
	itest.ApplyEchoService(ctx, serviceName, s.AppNamespace(), port)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", serviceName)

	aliasedService := fmt.Sprintf("%s.%s", serviceName, s.AppNamespace())
	mappings := []map[string]string{
		{
			"aliasFor": aliasedService,
			"name":     "my-alias",
		},
		{
			"aliasFor": aliasedService,
			"name":     fmt.Sprintf("my-alias.%s", s.AppNamespace()),
		},
		{
			"aliasFor": aliasedService,
			"name":     "my-alias.some-fantasist-root-domain.cluster.local",
		},
	}
	ctx = itest.WithKubeConfigExtension(ctx, func(cluster *api.Cluster) map[string]any {
		return map[string]any{"dns": map[string][]map[string]string{
			"mappings": mappings,
		}}
	})

	// when
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace(), "--context", "extra")

	// then
	for _, mapping := range mappings {
		s.Eventually(func() bool {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", mapping["name"], port), 5000*time.Millisecond)
			if err == nil {
				_ = conn.Close()
			}
			return err == nil
		}, 10*time.Second, 1*time.Second, "can find alias %s", mapping["name"])
	}
}
