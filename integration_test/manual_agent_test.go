package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

func (s *notConnectedSuite) Test_ManualAgent() {
	testManualAgent(&s.Suite, s)
}

func testManualAgent(s *itest.Suite, nsp itest.NamespacePair) {
	if !(s.ManagerIsVersion(">2.21.x") && s.ClientIsVersion(">2.21.x")) {
		s.T().Skip("Not part of compatibility tests. Manual setup changed in 2.22.0")
	}
	require := s.Require()
	ctx := s.Context()

	k8sDir := filepath.Join("testdata", "k8s")
	require.NoError(nsp.Kubectl(ctx, "apply", "-f", filepath.Join(k8sDir, "echo-manual-inject-svc.yaml")))

	agentImage := fmt.Sprintf("%s/%s:%s", s.AgentRegistry(), s.AgentImage(), s.AgentVersion())
	inputFile := filepath.Join(k8sDir, "echo-manual-inject-deploy.yaml")
	cfgEntry := itest.TelepresenceOk(ctx, "genyaml", "config",
		"--agent-image", agentImage,
		"--output", "-",
		"--manager-namespace", nsp.ManagerNamespace(),
		"--namespace", nsp.AppNamespace(),
		"--input", inputFile,
		"--loglevel", "debug")
	var ac agentconfig.Sidecar
	require.NoError(yaml.Unmarshal([]byte(cfgEntry), &ac))

	tmpDir := s.T().TempDir()
	writeYaml := func(name string, data any) string {
		yf := filepath.Join(tmpDir, name)
		b, err := yaml.Marshal(data)
		require.NoError(err)
		require.NoError(os.WriteFile(yf, b, 0o666))
		return yf
	}

	configFile := filepath.Join(tmpDir, ac.WorkloadName)
	require.NoError(os.WriteFile(configFile, []byte(cfgEntry), 0o666))

	stdout := itest.TelepresenceOk(ctx, "genyaml", "container",
		"--output", "-",
		"--config", configFile,
		"--input", filepath.Join(k8sDir, "echo-manual-inject-deploy.yaml"))
	var container map[string]any
	require.NoError(yaml.Unmarshal([]byte(stdout), &container))

	stdout = itest.TelepresenceOk(ctx, "genyaml", "initcontainer", "--output", "-", "--config", configFile)
	var initContainer map[string]any
	require.NoError(yaml.Unmarshal([]byte(stdout), &initContainer))

	stdout = itest.TelepresenceOk(ctx, "genyaml", "volume", "--config", configFile, "--input", inputFile)
	var volumes []map[string]any
	require.NoError(yaml.Unmarshal([]byte(stdout), &volumes))

	stdout = itest.TelepresenceOk(ctx, "genyaml", "annotations", "--config", configFile)
	var anns map[string]string
	require.NoError(yaml.Unmarshal([]byte(stdout), &anns))

	b, err := os.ReadFile(filepath.Join(k8sDir, "echo-manual-inject-deploy.yaml"))
	require.NoError(err)
	var deploy map[string]any
	err = yaml.Unmarshal(b, &deploy)
	require.NoError(err)

	renameHttpPort := func(con map[string]any) {
		if ports, ok := con["ports"].([]map[string]any); ok {
			for _, port := range ports {
				if port["name"] == "http" {
					port["name"] = "tm_http"
				}
			}
		}
	}

	podTemplate := deploy["spec"].(map[string]any)["template"].(map[string]any)
	podSpec := podTemplate["spec"].(map[string]any)
	cons := podSpec["containers"].([]any)
	for _, con := range cons {
		renameHttpPort(con.(map[string]any))
	}
	podSpec["containers"] = append(cons, container)
	podSpec["initContainers"] = []map[string]any{initContainer}
	podSpec["volumes"] = volumes
	podTemplate["metadata"].(map[string]any)["annotations"] = anns

	dplYaml := writeYaml("deployment.yaml", deploy)
	require.NoError(nsp.Kubectl(ctx, "apply", "-f", dplYaml))
	defer func() {
		require.NoError(nsp.Kubectl(ctx, "delete", "-f", dplYaml))
	}()

	err = nsp.RolloutStatusWait(ctx, "deploy/"+ac.WorkloadName)
	nsp.CapturePodLogs(ctx, ac.WorkloadName, "traffic-agent", nsp.AppNamespace())
	require.NoError(err)

	nsp.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	stdout = itest.TelepresenceOk(ctx, "list")
	require.Regexp(regexp.MustCompile(`.*`+ac.WorkloadName+`\s*:\s*ready to engage|intercept \(traffic-agent already installed\).*`), stdout)

	svcPort, svcCancel := itest.StartLocalHttpEchoServer(ctx, ac.WorkloadName)
	defer svcCancel()

	itest.TelepresenceOk(ctx, "intercept", ac.WorkloadName, "--port", strconv.Itoa(svcPort))
	defer itest.TelepresenceOk(ctx, "leave", ac.WorkloadName)

	s.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && strings.Contains(stdout, ac.WorkloadName+": intercepted")
	}, 30*time.Second, 3*time.Second)

	itest.PingInterceptedEchoServer(ctx, ac.WorkloadName, "80")
}
