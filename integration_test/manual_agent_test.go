package integration_test

import (
	"io"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

func (s *connectedSuite) Test_ManualAgent() {
	s.T().Skip("skipping until this has been figured out")
	require := s.Require()
	ctx := s.Context()

	k8sDir := filepath.Join(itest.GetWorkingDir(itest.WithModuleRoot(ctx)), "k8s")
	stdout := itest.TelepresenceOk(ctx, "genyaml", "container", "--container-name", "echo-container", "--port", "8080", "--output",
		"-", "--input", filepath.Join(k8sDir, "echo-manual-inject-deploy.yaml"))
	container := map[string]interface{}{}
	require.NoError(yaml.Unmarshal([]byte(stdout), &container))

	stdout = itest.TelepresenceOk(ctx, "genyaml", "volume", "--output",
		"-", "--input", filepath.Join(k8sDir, "echo-manual-inject-deploy.yaml"))
	volume := map[string]interface{}{}
	require.NoError(yaml.Unmarshal([]byte(stdout), &volume))

	f, err := os.Open(filepath.Join(k8sDir, "echo-manual-inject-deploy.yaml"))
	require.NoError(err)
	defer f.Close()
	b, err := io.ReadAll(f)
	require.NoError(err)
	deploy := map[string]interface{}{}
	require.NoError(yaml.Unmarshal(b, &deploy))

	podTemplate := deploy["spec"].(map[string]interface{})["template"].(map[string]interface{})
	podSpec := podTemplate["spec"].(map[string]interface{})
	cons := podSpec["containers"].([]interface{})
	podSpec["containers"] = append(cons, container)
	podSpec["volumes"] = []interface{}{volume}
	podTemplate["metadata"].(map[string]interface{})["annotations"] = map[string]string{install.ManualInjectAnnotation: "true"}

	f, err = os.Open(filepath.Join(k8sDir, "echo-manual-inject-svc.yaml"))
	require.NoError(err)
	defer f.Close()
	svc, err := io.ReadAll(f)
	require.NoError(err)

	tmpDir := s.T().TempDir()
	yamlFile := filepath.Join(tmpDir, "deployment.yaml")
	f, err = os.Create(yamlFile)
	require.NoError(err)
	_, err = f.Write(svc)
	require.NoError(err)
	_, err = f.Write([]byte("\n---\n"))
	require.NoError(err)
	b, err = yaml.Marshal(&deploy)
	require.NoError(err)
	_, err = f.Write(b)
	require.NoError(err)
	f.Close()

	require.NoError(s.Kubectl(ctx, "apply", "-f", yamlFile, "--context", "default"))
	defer func() {
		require.NoError(s.Kubectl(ctx, "delete", "-f", yamlFile, "--context", "default"))
	}()
	require.NoError(s.RolloutStatusWait(ctx, "deploy/manual-inject"))

	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace())
	require.Regexp(regexp.MustCompile(`.*manual-inject\s*:\s*ready to intercept \(traffic-agent already installed\).*`), stdout)

	itest.TelepresenceOk(ctx, "intercept", "manual-inject", "--namespace", s.AppNamespace(), "--port", "9094")
	itest.TelepresenceOk(ctx, "leave", "manual-inject-"+s.AppNamespace())
}
