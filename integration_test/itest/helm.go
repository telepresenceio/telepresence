package itest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbac "k8s.io/api/rbac/v1"
	sigsYaml "sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	telcharts "github.com/telepresenceio/telepresence/v2/charts"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func (s *cluster) PackageHelmChart(ctx context.Context) (string, error) {
	filename := filepath.Join(getT(ctx).TempDir(), "telepresence-chart.tgz")
	fh, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return "", err
	}
	if err := telcharts.WriteChart(telcharts.DirTypeTelepresence, fh, "telepresence", s.self.ClientVersion().String()); err != nil {
		_ = fh.Close()
		return "", err
	}
	if err := fh.Close(); err != nil {
		return "", err
	}
	return filename, nil
}

func (s *cluster) GetSetArgsForHelm(ctx context.Context, values map[string]any, release bool) []string {
	settings := s.GetValuesForHelm(ctx, values, release)
	args := make([]string, len(settings)*2)
	n := 0
	for _, s := range settings {
		args[n] = "--set-json"
		n++
		args[n] = s
		n++
	}
	return args
}

func (s *cluster) GetValuesForHelm(ctx context.Context, values map[string]any, release bool) []string {
	nss := GetNamespaces(ctx)
	settings := []string{
		`logLevel="debug"`,
	}

	if s.ManagerVersion().EQ(version.Structured) {
		reg := s.self.ManagerRegistry()
		if reg == "local" {
			settings = append(settings, `image.pullPolicy="Never"`)
			settings = append(settings, `agent.image.pullPolicy="Never"`)
		} else if !s.isCI {
			settings = append(settings, `image.pullPolicy="Always"`)
			settings = append(settings, `agent.image.pullPolicy="Always"`)
		}
	}
	if nss != nil && nss.Selector != nil {
		j, err := json.Marshal(nss.Selector)
		if err != nil {
			dlog.Errorf(ctx, "unable to marshal selector '%v': %v", nss.Selector, err)
		} else {
			settings = append(settings, `namespaceSelector=`+string(j))
		}
	}
	agentImage := GetAgentImage(ctx)
	if agentImage != nil {
		settings = append(settings,
			fmt.Sprintf(`agent.image.name=%q`, agentImage.Name), // Prevent attempts to retrieve image from SystemA
			fmt.Sprintf(`agent.image.tag=%q`, agentImage.Tag),
			fmt.Sprintf(`agent.image.registry=%q`, agentImage.Registry))
	}
	if !release {
		settings = append(settings, fmt.Sprintf(`image.registry=%q`, s.self.ManagerRegistry()))
	}

	for k, v := range values {
		j, err := json.Marshal(v)
		if err != nil {
			dlog.Errorf(ctx, "unable to marshal value %v: %v", v, err)
		} else {
			settings = append(settings, k+"="+string(j))
		}
	}
	return settings
}

func (s *cluster) TelepresenceHelmInstallOK(ctx context.Context, upgrade bool, settings ...string) string {
	logFile, err := s.self.TelepresenceHelmInstall(ctx, upgrade, settings...)
	require.NoError(getT(ctx), err)
	return logFile
}

func (s *cluster) TelepresenceHelmInstall(ctx context.Context, upgrade bool, settings ...string) (string, error) {
	nss := GetNamespaces(ctx)
	subjectNames := []string{TestUser}
	subjects := make([]rbac.Subject, len(subjectNames))
	for i, s := range subjectNames {
		subjects[i] = rbac.Subject{
			Kind:      "ServiceAccount",
			Name:      s,
			Namespace: nss.Namespace,
		}
	}

	type xRbac struct {
		Create     bool           `json:"create"`
		Namespaced bool           `json:"namespaced"`
		Subjects   []rbac.Subject `json:"subjects,omitempty"`
		Namespaces []string       `json:"namespaces,omitempty"`
	}
	type xAgent struct {
		Image *Image `json:"image,omitempty"`
	}
	agentImage := GetAgentImage(ctx)
	agent := &xAgent{Image: agentImage}
	type xClient struct {
		Routing map[string][]string `json:"routing"`
	}
	type xTimeouts struct {
		AgentArrival string `json:"agentArrival,omitempty"`
	}
	managerRbac := xRbac{
		Create: true,
	}
	clientRbac := xRbac{
		Create:   true,
		Subjects: subjects,
	}
	vx := struct {
		LogLevel          string           `json:"logLevel"`
		Image             *Image           `json:"image,omitempty"`
		Agent             *xAgent          `json:"agent,omitempty"`
		ClientRbac        *xRbac           `json:"clientRbac"`
		ManagerRbac       *xRbac           `json:"managerRbac"`
		Client            xClient          `json:"client"`
		Timeouts          xTimeouts        `json:"timeouts,omitempty"`
		Namespaces        []string         `json:"namespaces,omitempty"`
		NamespaceSelector *labels.Selector `json:"namespaceSelector,omitempty"`
	}{
		LogLevel:    "debug",
		Agent:       agent,
		ClientRbac:  &clientRbac,
		ManagerRbac: &managerRbac,
		Client: xClient{
			Routing: map[string][]string{},
		},
		Timeouts: xTimeouts{AgentArrival: "60s"},
	}
	if managedNamespaces := nss.Selector.StaticNames(); len(managedNamespaces) > 0 {
		if s.ManagerIsVersion(">2.21.x") {
			vx.Namespaces = managedNamespaces
		} else {
			// Older versions require the TestUser to have update permission on the telepresence-agents configmap.
			if !slices.Contains(managedNamespaces, nss.Namespace) {
				managedNamespaces = append(managedNamespaces, nss.Namespace)
			}
			clientRbac.Namespaced = true
			clientRbac.Namespaces = managedNamespaces
			managerRbac.Namespaced = true
			managerRbac.Namespaces = managedNamespaces
		}
		for _, ns := range managedNamespaces {
			err := Kubectl(ctx, ns, "create", "role", "tele-update-config", "--verb=update", "--resource=configmaps", "--resource-name=telepresence-agents")
			if err != nil && !strings.Contains(err.Error(), "already exists") {
				return "", err
			}
			err = Kubectl(ctx, ns, "create", "rolebinding", "tele-update-config",
				"--role=tele-update-config", "--serviceaccount="+nss.Namespace+":"+TestUser)
			if err != nil && !strings.Contains(err.Error(), "already exists") {
				return "", err
			}
		}
	} else {
		vx.NamespaceSelector = nss.Selector
	}

	vx.Image = GetImage(ctx)
	if !s.isCI && s.ManagerVersion().EQ(s.ClientVersion()) {
		pp := "Always"
		if s.ManagerRegistry() == "local" {
			// Using minikube with local images.
			// They are automatically present and must not be pulled.
			pp = "Never"
		}
		vx.Image.PullPolicy = pp
		vx.Agent.Image.PullPolicy = pp
	}

	ss, err := sigsYaml.Marshal(&vx)
	if err != nil {
		return "", err
	}
	valuesFile := filepath.Join(getT(ctx).TempDir(), "values.yaml")
	if err := os.WriteFile(valuesFile, ss, 0o644); err != nil {
		return "", err
	}

	verb := "install"
	if upgrade {
		verb = "upgrade"
	}
	args := []string{"helm", verb, "-n", nss.Namespace, "-f", valuesFile}
	if !s.ManagerVersion().EQ(version.Structured) {
		args = append(args, "--version", s.ManagerVersion().String())
	}
	args = append(args, settings...)

	if _, _, err = Telepresence(WithUser(ctx, "default"), args...); err != nil {
		return "", err
	}
	if err = RolloutStatusWait(ctx, nss.Namespace, "deploy/"+agentmap.ManagerAppName); err != nil {
		return "", err
	}
	logFileName := s.self.CapturePodLogs(ctx, agentmap.ManagerAppName, "", nss.Namespace)

	if !s.ManagerIsVersion(">2.21.x") {
		// Give the manager time to perform rollouts, listen to telepresence-agents configmap, etc.
		time.Sleep(2 * time.Second)
	}
	return logFileName, nil
}

func (s *cluster) UninstallTrafficManager(ctx context.Context, managerNamespace string, args ...string) {
	t := getT(ctx)
	ctx = WithUser(ctx, "default")
	TelepresenceOk(ctx, append([]string{"helm", "uninstall", "--manager-namespace", managerNamespace}, args...)...)

	// Helm uninstall does deletions asynchronously, so let's wait until the deployment is gone
	assert.Eventually(t, func() bool { return len(RunningPodNames(ctx, agentmap.ManagerAppName, managerNamespace)) == 0 },
		60*time.Second, 4*time.Second, "traffic-manager deployment was not removed")
	TelepresenceQuitOk(ctx)
}
