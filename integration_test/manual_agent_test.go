package integration_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

func (s *connectedSuite) Test_ManualAgent() {
	testManualAgent(&s.Suite, s.NamespacePair)
}

func testManualAgent(s *itest.Suite, nsp itest.NamespacePair) {
	require := s.Require()
	ctx := s.Context()

	k8sDir := filepath.Join("testdata", "k8s")
	require.NoError(nsp.Kubectl(ctx, "apply", "-f", filepath.Join(k8sDir, "echo-manual-inject-svc.yaml")))

	agentImage := s.Registry() + "/tel2:" + strings.TrimPrefix(s.TelepresenceVersion(), "v")
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
	podTemplate["metadata"].(map[string]any)["annotations"] = map[string]string{mutator.ManualInjectAnnotation: "true"}

	// Add the configmap entry by first retrieving the current config map
	var cfgMap *core.ConfigMap
	origCfgYaml, err := nsp.KubectlOut(ctx, "get", "configmap", agentconfig.ConfigMap, "-o", "yaml")
	if err != nil {
		cfgMap = &core.ConfigMap{
			TypeMeta: meta.TypeMeta{
				Kind:       "ConfigMap",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      agentconfig.ConfigMap,
				Namespace: nsp.AppNamespace(),
			},
		}
		origCfgYaml = ""
	} else {
		require.NoError(yaml.Unmarshal([]byte(origCfgYaml), &cfgMap))
	}
	if cfgMap.Data == nil {
		cfgMap.Data = make(map[string]string)
	}
	cfgMap.Data[ac.WorkloadName] = cfgEntry

	cfgYaml := writeYaml(agentconfig.ConfigMap+".yaml", cfgMap)
	require.NoError(nsp.Kubectl(ctx, "apply", "-f", cfgYaml))
	defer func() {
		if origCfgYaml == "" {
			require.NoError(nsp.Kubectl(ctx, "delete", "configmap", agentconfig.ConfigMap))
		} else {
			// Restore original configmap
			cfgMap.ObjectMeta = meta.ObjectMeta{
				Name:      agentconfig.ConfigMap,
				Namespace: nsp.AppNamespace(),
			}
			writeYaml(agentconfig.ConfigMap+".yaml", cfgMap)
		}
	}()

	dplYaml := writeYaml("deployment.yaml", deploy)
	require.NoError(nsp.Kubectl(ctx, "apply", "-f", dplYaml))
	defer func() {
		require.NoError(nsp.Kubectl(ctx, "delete", "-f", dplYaml))
	}()

	err = nsp.RolloutStatusWait(ctx, "deploy/"+ac.WorkloadName)
	require.NoError(err)

	stdout = itest.TelepresenceOk(ctx, "list")
	require.Regexp(regexp.MustCompile(`.*`+ac.WorkloadName+`\s*:\s*ready to intercept \(traffic-agent already installed\).*`), stdout)

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
