package agent_test

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/dos/aferofs"
)

const serviceName = "test-echo"
const namespace = "teltest"
const podIP = "192.168.50.34"

var testConfig = agentconfig.Sidecar{
	Create:       false,
	AgentImage:   "docker.io/datawire/tel2:2.5.4",
	AgentName:    "test-echo",
	LogLevel:     "debug",
	Namespace:    namespace,
	WorkloadName: "test-echo",
	WorkloadKind: "Deployment",
	ManagerHost:  "traffic-manager.ambassador",
	ManagerPort:  8081,
	APIPort:      0,
	Containers: []*agentconfig.Container{{
		Name:       "test-echo",
		EnvPrefix:  "A_",
		MountPoint: "/tel_app_mounts/test-echo",
		Mounts:     []string{"/home/bob"},
		Intercepts: []*agentconfig.Intercept{
			{
				ContainerPortName: "http",
				ServiceName:       serviceName,
				ServiceUID:        "",
				ServicePortName:   "http",
				ServicePort:       80,
				Protocol:          "TCP",
				AgentPort:         9900,
				ContainerPort:     8080,
			},
		},
	}},
}

func testContext(t *testing.T, env dos.MapEnv) context.Context {
	fs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
	if env == nil {
		env = make(dos.MapEnv)
	}
	y, err := yaml.Marshal(&testConfig)
	require.NoError(t, err)

	require.NoError(t, fs.MkdirAll(agentconfig.ConfigMountPoint, 0700))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(agentconfig.ConfigMountPoint, agentconfig.ConfigFile), y, 0600))

	env[agentconfig.EnvPrefixAgent+"POD_IP"] = podIP

	ctx := dlog.NewTestContext(t, false)
	ctx = dos.WithFS(ctx, aferofs.Wrap(fs))
	return dos.WithEnv(ctx, env)
}

func Test_LoadConfig(t *testing.T) {
	ctx := testContext(t, nil)
	config, err := agent.LoadConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, &testConfig, config.AgentConfig())
	require.Equal(t, podIP, config.PodIP())
}

func Test_AppEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipped on windows")
	}
	ctx := testContext(t, dos.MapEnv{
		"HOME": "/home/tel",                    // skip
		"PATH": "/bin:/usr/bin:/usr/local/bin", // skip
		"ZULU": "zulu",                         // include,
		agentconfig.EnvPrefixApp + "A_" + "ALPHA": "alpha", // include
		agentconfig.EnvPrefixApp + "B_" + "BRAVO": "bravo", // skip
	})

	ksDir := "/var/run/secrets/kubernetes.io/serviceaccount"
	require.NoError(t, dos.MkdirAll(ctx, ksDir, 0700))
	f, err := dos.Create(ctx, filepath.Join(ksDir, "namespace"))
	require.NoError(t, err)
	_, err = fmt.Fprintln(f, "default")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	config, err := agent.LoadConfig(ctx)
	require.NoError(t, err)

	cn := config.AgentConfig().Containers[0]
	env, err := agent.AppEnvironment(ctx, cn)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"ALPHA":                           "alpha",
		"ZULU":                            "zulu",
		agentconfig.EnvInterceptContainer: "test-echo",
		agentconfig.EnvInterceptMounts:    "/home/bob:/var/run/secrets/kubernetes.io",
	}, env)

	// Check symlink to container's remote mount point
	f, err = dos.Open(ctx, filepath.Join(cn.MountPoint, ksDir, "namespace"))
	require.NoError(t, err, "not symlinked")
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.Equal(t, "default\n", string(data))
}
