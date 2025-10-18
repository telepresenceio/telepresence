package integration_test

import (
	"encoding/json"
	"time"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type workloadPortsSuite struct {
	itest.Suite
	itest.TrafficManager
}

func (s *workloadPortsSuite) SuiteName() string {
	return "WorkloadPorts"
}

func init() {
	itest.AddTrafficManagerSuite("-workload-ports", func(h itest.TrafficManager) itest.TestingSuite {
		return &workloadPortsSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

// SetupSuite creates workloads BEFORE connecting telepresence
// This ensures workloads are in the initial WatchWorkloads snapshot
func (s *workloadPortsSuite) SetupSuite() {
	s.Suite.SetupSuite()

	ctx := s.Context()

	// Deploy echo-easy (Deployment with normal service)
	s.ApplyApp(ctx, "echo-easy", "deploy/echo-easy")

	// Deploy echo-headless (StatefulSet with headless service)
	s.ApplyApp(ctx, "echo-headless", "statefulset/echo-headless")

	// Wait for workloads to be ready
	s.Eventually(func() bool {
		deployReady, err := s.KubectlOut(ctx, "get", "deploy/echo-easy", "-o", "jsonpath={.status.readyReplicas}")
		if err != nil || deployReady != "1" {
			return false
		}

		stsReady, err := s.KubectlOut(ctx, "get", "statefulset/echo-headless", "-o", "jsonpath={.status.readyReplicas}")
		return err == nil && stsReady == "1"
	}, 3*time.Minute, 3*time.Second)

	// NOW connect telepresence - workloads already exist and will be in initial snapshot
	s.TelepresenceConnect(ctx)
}

func (s *workloadPortsSuite) TearDownSuite() {
	ctx := s.Context()

	itest.TelepresenceDisconnectOk(ctx)

	// Clean up workloads
	s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
	s.DeleteSvcAndWorkload(ctx, "statefulset", "echo-headless")
}

func (s *workloadPortsSuite) Test_ListWithPorts() {
	ctx := s.Context()

	// Get the list output as JSON
	stdout, stderr, err := itest.Telepresence(ctx, "list", "--output", "json")
	s.Require().NoErrorf(err, "telepresence list failed: %s", stderr)

	// Parse the JSON response
	var snapshot connector.WorkloadInfoSnapshot
	s.Require().NoError(json.Unmarshal([]byte(stdout), &snapshot))

	// Find the echo-easy workload
	var foundWorkload *connector.WorkloadInfo
	for _, workload := range snapshot.Workloads {
		if workload.Name == "echo-easy" {
			foundWorkload = workload
			break
		}
	}

	s.Require().NotNil(foundWorkload, "workload echo-easy not found in list output")

	// Verify that the workload has ports information
	s.Require().NotEmpty(foundWorkload.Ports, "workload should have ports information")

	// Verify each port has the required fields
	for _, port := range foundWorkload.Ports {
		s.Require().Greater(port.ContainerPort, int32(0), "container port number should be greater than 0")
		s.Require().NotEmpty(port.Protocol, "port protocol should not be empty")
	}
}

func (s *workloadPortsSuite) Test_ListWithServicePorts() {
	ctx := s.Context()

	// Get the list output as JSON
	stdout, stderr, err := itest.Telepresence(ctx, "list", "--output", "json")
	s.Require().NoErrorf(err, "telepresence list failed: %s", stderr)

	// Parse the JSON response
	var snapshot connector.WorkloadInfoSnapshot
	s.Require().NoError(json.Unmarshal([]byte(stdout), &snapshot))

	// Find the echo-easy workload
	var foundWorkload *connector.WorkloadInfo
	for _, workload := range snapshot.Workloads {
		if workload.Name == "echo-easy" {
			foundWorkload = workload
			break
		}
	}

	s.Require().NotNil(foundWorkload, "workload echo-easy not found in list output")
	s.Require().NotEmpty(foundWorkload.Ports, "workload should have ports information")

	// Verify that both container and service port fields are present
	// For a normal service (not headless), we expect service ports to be populated
	hasServicePort := false
	for _, port := range foundWorkload.Ports {
		s.Require().Greater(port.ContainerPort, int32(0), "container port should be greater than 0")
		s.Require().NotEmpty(port.Protocol, "protocol should not be empty")

		// For a service with port mapping, service port should be populated
		// (it may be 0 for headless services, but echo-easy has a normal service)
		if port.ServicePort > 0 {
			hasServicePort = true
		}
	}

	// echo-easy has a normal service, so at least one port should have a service port
	s.True(hasServicePort, "workload with a normal service should have at least one service port populated")
}

func (s *workloadPortsSuite) Test_ListWithHeadlessServicePorts() {
	ctx := s.Context()

	// Get the list output as JSON
	stdout, stderr, err := itest.Telepresence(ctx, "list", "--output", "json")
	s.Require().NoErrorf(err, "telepresence list failed: %s", stderr)

	// Parse the JSON response
	var snapshot connector.WorkloadInfoSnapshot
	s.Require().NoError(json.Unmarshal([]byte(stdout), &snapshot))

	// Find the echo-headless workload
	var foundWorkload *connector.WorkloadInfo
	for _, workload := range snapshot.Workloads {
		if workload.Name == "echo-headless" {
			foundWorkload = workload
			break
		}
	}

	s.Require().NotNil(foundWorkload, "workload echo-headless not found in list output")
	s.Require().NotEmpty(foundWorkload.Ports, "headless workload should have ports information")

	// Verify that container ports are always present
	// For headless services, the behavior may vary:
	// - Container ports are always populated (mandatory)
	// - Service ports might be 0 or might match container ports depending on implementation
	for _, port := range foundWorkload.Ports {
		s.Require().Greater(port.ContainerPort, int32(0), "container port should always be greater than 0")
		s.Require().NotEmpty(port.Protocol, "protocol should not be empty")
	}

	// The key assertion: container ports must be present for headless services
	s.GreaterOrEqual(len(foundWorkload.Ports), 1, "headless service should have at least one container port")
}
