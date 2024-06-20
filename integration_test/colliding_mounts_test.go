package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type mountsSuite struct {
	itest.Suite
	itest.NamespacePair
	eksClusterName string
}

func (s *mountsSuite) SuiteName() string {
	return "Mounts"
}

func init() {
	itest.AddConnectedSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &mountsSuite{
			Suite:         itest.Suite{Harness: h},
			NamespacePair: h,
		}
	})
}

// testIamServiceAccount is the serviceaccount denoted in the "hello-w-volumes" deployment (you'll
// find it under testdata/k8s). It's this serviceaccount that triggers the EKS injection of the
// "eks.amazonaws.com" folder under "/var/run/secrets" when using EKS.
const testIamServiceAccount = "mount-test-account"

// dnsPolicy is just a policy that is attached to the testIamServiceAccount. It's
// otherwise insignificant and not really used.
const dnsPolicy = "ExternalDNS-EKS"

// createIAMServiceAccount is only supposed do work when using an EKS cluster.
func (s *mountsSuite) createIAMServiceAccount() (string, error) {
	ctx := s.Context()
	var clusterName string
	out, err := itest.KubectlOut(ctx, "", "config", "current-context")
	if err == nil && strings.HasPrefix(out, "arn:aws:eks:") {
		out = strings.TrimSpace(out)
		nameSep := strings.IndexByte(out, '/')
		if nameSep > 0 {
			clusterName = out[nameSep+1:]
		}
	}
	if clusterName == "" {
		return "", errors.New("can't parse EKS cluster name")
	}

	out, err = itest.Output(ctx, "aws", "sts", "get-caller-identity", "--output", "json")
	if err != nil {
		return "", err
	}
	var idMap map[string]string
	if err = json.Unmarshal([]byte(out), &idMap); err != nil {
		return "", err
	}
	return clusterName, itest.Run(ctx, "eksctl", "create", "iamserviceaccount",
		"--name", testIamServiceAccount,
		"--namespace", s.AppNamespace(),
		"--cluster", clusterName,
		"--attach-policy-arn", fmt.Sprintf("arn:aws:iam::%s:policy/%s", idMap["Account"], dnsPolicy),
		"--approve")
}

func (s *mountsSuite) SetupSuite() {
	if s.IsCI() && runtime.GOOS == "darwin" {
		s.T().Skip("Mount tests don't run on darwin due to macFUSE issues")
		return
	}
	s.Suite.SetupSuite()
	var err error
	s.eksClusterName, err = s.createIAMServiceAccount()
	if err != nil {
		dlog.Infof(s.Context(), "could not create iamserviceaccount: %v. Creating a normal serviceaccount instead", err)
		s.NoError(itest.Kubectl(s.Context(), s.AppNamespace(), "create", "serviceaccount", testIamServiceAccount))
	}
}

func (s *mountsSuite) TearDownSuite() {
	if s.eksClusterName != "" {
		s.NoError(itest.Run(s.Context(), "eksctl", "delete", "iamserviceaccount",
			"--name", testIamServiceAccount,
			"--namespace", s.AppNamespace(),
			"--cluster", s.eksClusterName))
	}
}

// Test_CollidingMounts tests that multiple mounts from several containers are managed correctly
// by the traffic-agent and that an intercept of a container mounts the expected volumes.
//
// When an EKS cluster is used and the iamserviceaccount denoted by testIamServiceAccount could
// be successfully created, EKS will mount an "eks.amazonaws.com" folder under "/var/run/secrets"
// using a mutating admission controller (because the "hello-w-volumes" deployment uses that
// serviceaccount). This test verifies that this folder is available during intercept.
func (s *mountsSuite) Test_CollidingMounts() {
	ctx := s.Context()
	s.ApplyApp(ctx, "hello-w-volumes", "deploy/hello")
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "hello")

	type lm struct {
		name       string
		svcPort    int
		mountPoint string
	}
	var tests []lm
	if runtime.GOOS == "windows" {
		tests = []lm{
			{
				"one",
				80,
				"O:",
			},
			{
				"two",
				81,
				"T:",
			},
		}
	} else {
		tempDir := s.T().TempDir()
		tests = []lm{
			{
				"one",
				80,
				filepath.Join(tempDir, "one"),
			},
			{
				"two",
				81,
				filepath.Join(tempDir, "two"),
			},
		}
	}

	for i, tt := range tests {
		i := i
		tt := tt
		s.Run(tt.name, func() {
			ctx := s.Context()
			require := s.Require()
			stdout := itest.TelepresenceOk(ctx, "intercept", "hello", "--mount", tt.mountPoint, "--port", fmt.Sprintf("%d:%d", tt.svcPort, tt.svcPort))
			defer itest.TelepresenceOk(ctx, "leave", "hello")
			require.Contains(stdout, "Using Deployment hello")
			if i == 0 {
				s.CapturePodLogs(ctx, "hello", "traffic-agent", s.AppNamespace())
			} else {
				// Mounts are sometimes slow
				dtime.SleepWithContext(ctx, 3*time.Second)
			}
			ns, err := os.ReadFile(filepath.Join(tt.mountPoint, "var", "run", "secrets", "kubernetes.io", "serviceaccount", "namespace"))
			require.NoError(err)
			require.Equal(s.AppNamespace(), string(ns))
			un, err := os.ReadFile(filepath.Join(tt.mountPoint, "var", "run", "secrets", "datawire.io", "auth", "username"))
			require.NoError(err)
			require.Equal(fmt.Sprintf("hello-%d", i+1), string(un))
			if s.eksClusterName != "" {
				token, err := os.ReadFile(filepath.Join(tt.mountPoint, "var", "run", "secrets", "eks.amazonaws.com", "serviceaccount", "token"))
				require.NoError(err)
				require.True(len(token) > 0)
			}
		})
	}
}
