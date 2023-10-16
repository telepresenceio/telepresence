package integration_test

import (
	"os"
	"path/filepath"
	"regexp"
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
	require := s.Require()
	assert := s.Assert()
	ctx := s.Context()

	k8sDir := filepath.Join("testdata", "k8s")
	require.NoError(itest.Kubectl(ctx, s.AppNamespace(), "apply", "-f", filepath.Join(k8sDir, "echo-manual-inject-svc.yaml")))

	agentImage := s.Registry() + "/tel2:" + strings.TrimPrefix(s.TelepresenceVersion(), "v")
	inputFile := filepath.Join(k8sDir, "echo-manual-inject-deploy.yaml")
	cfgEntry := itest.TelepresenceOk(ctx, "genyaml", "config",
		"--agent-image", agentImage,
		"--output", "-",
		"--manager-namespace", s.ManagerNamespace(),
		"--namespace", s.AppNamespace(),
		"--input", inputFile,
		"--loglevel", "debug")
	var ac agentconfig.Sidecar
	require.NoError(yaml.Unmarshal([]byte(cfgEntry), &ac))

	tmpDir := s.T().TempDir()
	writeFile := func(file string, data []byte) {
		f, err := os.Create(file)
		require.NoError(err)
		defer f.Close()
		_, err = f.Write(data)
		assert.NoError(err)
	}

	writeYaml := func(name string, data any) string {
		yf := filepath.Join(tmpDir, name)
		b, err := yaml.Marshal(data)
		require.NoError(err)
		writeFile(yf, b)
		return yf
	}

	configFile := filepath.Join(tmpDir, ac.WorkloadName)
	writeFile(configFile, []byte(cfgEntry))

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
	origCfgYaml, err := s.KubectlOut(ctx, "get", "configmap", agentconfig.ConfigMap, "-o", "yaml")
	if err != nil {
		cfgMap = &core.ConfigMap{
			TypeMeta: meta.TypeMeta{
				Kind:       "ConfigMap",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      agentconfig.ConfigMap,
				Namespace: s.AppNamespace(),
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
	require.NoError(s.Kubectl(ctx, "apply", "-f", cfgYaml))
	defer func() {
		if origCfgYaml == "" {
			require.NoError(s.Kubectl(ctx, "delete", "configmap", agentconfig.ConfigMap))
		} else {
			// Restore original configmap
			cfgMap.ObjectMeta = meta.ObjectMeta{
				Name:      agentconfig.ConfigMap,
				Namespace: s.AppNamespace(),
			}
			cleanMapYaml, err := yaml.Marshal(cfgMap)
			require.NoError(err)
			writeFile(cfgYaml, cleanMapYaml)
			require.NoError(s.Kubectl(ctx, "apply", "-f", cfgYaml))
		}
	}()

	dplYaml := writeYaml("deployment.yaml", deploy)
	require.NoError(s.Kubectl(ctx, "apply", "-f", dplYaml))
	defer func() {
		require.NoError(s.Kubectl(ctx, "delete", "-f", dplYaml))
	}()

	err = s.RolloutStatusWait(ctx, "deploy/"+ac.WorkloadName)
	require.NoError(err)

	stdout = itest.TelepresenceOk(ctx, "list")
	require.Regexp(regexp.MustCompile(`.*`+ac.WorkloadName+`\s*:\s*ready to intercept \(traffic-agent already installed\).*`), stdout)

	itest.TelepresenceOk(ctx, "intercept", ac.WorkloadName, "--port", "9094")
	s.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && strings.Contains(stdout, ac.WorkloadName+": intercepted")
	}, 30*time.Second, 3*time.Second)
	itest.TelepresenceOk(ctx, "leave", ac.WorkloadName)
}
