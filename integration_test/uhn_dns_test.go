package integration_test

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
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
	port, svcCancel := itest.StartLocalHttpEchoServer(ctx, serviceName)
	defer svcCancel()

	itest.ApplyEchoService(ctx, serviceName, s.AppNamespace(), port)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", serviceName)

	excludes := []string{
		"echo",
		fmt.Sprintf("echo.%s", s.AppNamespace()),
		fmt.Sprintf("echo.%s.svc.cluster.local", s.AppNamespace()),
	}
	ctx = itest.WithKubeConfigExtension(ctx, func(cluster *api.Cluster) map[string]any {
		return map[string]any{"dns": map[string][]string{
			"excludes": excludes,
		}}
	})

	// when
	s.TelepresenceConnect(ctx, "--context", "extra")
	itest.TelepresenceOk(ctx, "intercept", serviceName, "--port", strconv.Itoa(port))

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

	status := itest.TelepresenceStatusOk(s.Context())
	assert.Equal(s.T(), excludes, status.RootDaemon.DNS.Excludes, "Excludes in output")
}

func (s *unqualifiedHostNameDNSSuite) Test_UHNMappings() {
	// given
	ctx := s.Context()
	serviceName := "echo"
	port := 80
	itest.ApplyEchoService(ctx, serviceName, s.AppNamespace(), port)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", serviceName)

	aliasedService := fmt.Sprintf("%s.%s", serviceName, s.AppNamespace())
	dnsMappings := client.DNSMappings{
		{
			Name:     "my-alias",
			AliasFor: aliasedService,
		},
		{
			Name:     fmt.Sprintf("my-alias.%s", s.AppNamespace()),
			AliasFor: aliasedService,
		},
		{
			Name:     "my-alias.some-fantasist-root-domain.cluster.local",
			AliasFor: aliasedService,
		},
	}
	mappings := make([]map[string]string, 3)
	for i, dm := range dnsMappings {
		mappings[i] = map[string]string{"name": dm.Name, "aliasFor": dm.AliasFor}
	}
	ctx = itest.WithKubeConfigExtension(ctx, func(cluster *api.Cluster) map[string]any {
		return map[string]any{"dns": map[string]client.DNSMappings{
			"mappings": dnsMappings,
		}}
	})

	// when
	s.TelepresenceConnect(ctx, "--context", "extra")

	// then
	for _, mapping := range dnsMappings {
		s.Eventually(func() bool {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", mapping.Name, port), 5000*time.Millisecond)
			if err == nil {
				_ = conn.Close()
			}
			return err == nil
		}, 10*time.Second, 1*time.Second, "can find alias %s", mapping.Name)
	}

	status := itest.TelepresenceStatusOk(s.Context())
	assert.Equal(s.T(), dnsMappings, status.RootDaemon.DNS.Mappings, "Mappings in output")
}
