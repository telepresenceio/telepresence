package itest

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

func ApplyEchoService(ctx context.Context, name, namespace string, port int) {
	ApplyService(ctx, name, namespace, "jmalloc/echo-server:0.1.0", port, 8080)
}

func ApplyService(ctx context.Context, name, namespace, image string, port, targetPort int) {
	t := getT(ctx)
	t.Helper()
	require.NoError(t, Kubectl(ctx, namespace, "create", "deploy", name, "--image", image), "failed to create deployment %s", name)
	require.NoError(t, Kubectl(ctx, namespace, "expose", "deploy", name, "--port", strconv.Itoa(port), "--target-port", strconv.Itoa(targetPort)),
		"failed to expose deployment %s", name)
	require.NoError(t, Kubectl(ctx, namespace, "rollout", "status", "-w", "deployment/"+name), "failed to deploy %s", name)
}

func DeleteSvcAndWorkload(ctx context.Context, workload, name, namespace string) {
	assert.NoError(getT(ctx), Kubectl(ctx, namespace, "delete", "--ignore-not-found", "--grace-period", "3", "svc,"+workload, name),
		"failed to delete service and %s %s", workload, name)
}

// ApplyApp calls kubectl apply -n <namespace> -f on the given app + .yaml found in testdata/k8s relative
// to the directory returned by GetWorkingDir.
func ApplyApp(ctx context.Context, name, namespace, workload string) {
	t := getT(ctx)
	t.Helper()
	manifest := filepath.Join("testdata", "k8s", name+".yaml")
	require.NoError(t, Kubectl(ctx, namespace, "apply", "-f", manifest), "failed to apply %s", manifest)
	require.NoError(t, RolloutStatusWait(ctx, namespace, workload))
}

type AppPort struct {
	ServicePortName   string
	ServicePortNumber uint16
	TargetPortName    string
	TargetPortNumber  uint16
	Protocol          string
	AppProtocol       string
}
type AppData struct {
	ServiceName    string
	DeploymentName string
	AppName        string
	ContainerName  string
	Image          string
	PullPolicy     string
	Ports          []AppPort
	Env            map[string]string
}

// ApplyAppTemplate calls kubectl apply -n <namespace> -f on the given app + .yaml found in testdata/k8s relative
// to the directory returned by GetWorkingDir.
func ApplyAppTemplate(ctx context.Context, namespace string, app *AppData) {
	t := getT(ctx)
	t.Helper()
	r, err := OpenTemplate(WithWorkingDir(ctx, filepath.Join(GetOSSRoot(ctx), "testdata", "k8s")), "svc-deploy.goyaml", app)
	require.NoError(t, err)
	require.NoError(t, Kubectl(dos.WithStdin(ctx, r), namespace, "apply", "-f", "-"), "failed to apply template")
	wl := app.DeploymentName
	if wl == "" {
		wl = app.AppName
	}
	require.NoError(t, RolloutStatusWait(ctx, namespace, "deploy/"+wl))
}

func RolloutStatusWait(ctx context.Context, namespace, workload string) error {
	ctx, cancel := context.WithTimeout(ctx, PodCreateTimeout(ctx))
	defer cancel()
	switch {
	case strings.HasPrefix(workload, "pod/"):
		return Kubectl(ctx, namespace, "wait", workload, "--for", "condition=ready")
	case strings.HasPrefix(workload, "replicaset/"), strings.HasPrefix(workload, "statefulset/"):
		for {
			status := struct {
				ReadyReplicas int `json:"readyReplicas,omitempty"`
				Replicas      int `json:"replicas,omitempty"`
			}{}
			stdout, err := KubectlOut(ctx, namespace, "get", workload, "-o", "jsonpath={..status}")
			if err != nil {
				return err
			}
			if err = json.Unmarshal([]byte(stdout), &status); err != nil {
				return err
			}
			if status.ReadyReplicas == status.Replicas {
				return nil
			}
			dtime.SleepWithContext(ctx, 3*time.Second)
		}
	}
	return Kubectl(ctx, namespace, "rollout", "status", "-w", workload)
}
