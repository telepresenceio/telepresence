package integration_test

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/stretchr/testify/assert"
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
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace(), "--context", "extra")
	itest.TelepresenceOk(ctx, "intercept", serviceName, "--namespace", s.AppNamespace(), "--port", strconv.Itoa(port))

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

	var status statusResponse
	stdout := itest.TelepresenceOk(s.Context(), "status", "--output", "json")
	assert.NoError(s.T(), json.Unmarshal([]byte(stdout), &status), "Output can be parsed")
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

	var status statusResponse
	stdout := itest.TelepresenceOk(s.Context(), "status", "--output", "json")
	assert.NoError(s.T(), json.Unmarshal([]byte(stdout), &status), "Output can be parsed")
	assert.Equal(s.T(), mappings, status.RootDaemon.DNS.Mappings, "Mappings in output")
}
