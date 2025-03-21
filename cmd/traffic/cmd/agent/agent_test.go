package agent_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/dos/aferofs"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

const (
	serviceName = "test-echo"
	namespace   = "teltest"
	podIP       = "192.168.50.34"
	podName     = "test-echo-f4784865-9wgqz"
	podUID      = "dc6100d6-2316-4eb6-9e99-9bf349877fb8"
)

var testConfig = agentconfig.Sidecar{
	Create:       false,
	AgentImage:   "ghcr.io/telepresenceio/tel2:2.5.4",
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
		MountPaths: []string{"/home/bob"},
		Mounts: map[string]types.MountPolicy{
			"/tmp":          types.MountPolicyLocal,
			"/home/bob":     types.MountPolicyRemote,
			"/home/brianna": types.MountPolicyRemoteReadOnly,
		},
		Intercepts: []*agentconfig.Intercept{
			{
				ContainerPortName: "http",
				ServiceName:       serviceName,
				ServiceUID:        "",
				ServicePortName:   "http",
				ServicePort:       80,
				Protocol:          core.ProtocolTCP,
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

	require.NoError(t, fs.MkdirAll(agentconfig.ExportsMountPoint, 0o700))

	home := filepath.Join("/tel_app_mounts/test-echo", "home")
	require.NoError(t, fs.MkdirAll(filepath.Join(home, "bob"), 0o700))
	require.NoError(t, fs.MkdirAll(filepath.Join(home, "brianna"), 0o700))

	cfgJSON, err := agentconfig.MarshalTight(&testConfig)
	require.NoError(t, err)

	env[agentconfig.EnvPrefixAgent+"NAME"] = serviceName
	env[agentconfig.EnvPrefixAgent+"POD_IP"] = podIP
	env[agentconfig.EnvPrefixAgent+"POD_NAME"] = podName
	env[agentconfig.EnvPrefixAgent+"POD_UID"] = podUID
	env[agentconfig.EnvAgentConfig] = cfgJSON

	ctx := dlog.NewTestContext(t, false)
	ctx = dos.WithFS(ctx, aferofs.Wrap(fs))
	return dos.WithEnv(ctx, env)
}

func Test_LoadConfig(t *testing.T) {
	ctx := testContext(t, nil)
	config, err := agent.LoadConfig(ctx)
	require.NoError(t, err)
	testConfig.AgentImage = ""
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

	config, err := agent.LoadConfig(ctx)
	require.NoError(t, err)

	cn := config.AgentConfig().Containers[0]
	env, err := agent.AppEnvironment(ctx, cn.Mounts, cn)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"ALPHA":                           "alpha",
		"ZULU":                            "zulu",
		agentconfig.EnvInterceptContainer: "test-echo",
		agentconfig.EnvInterceptMounts:    "/home/bob:/home/brianna",
		agentconfig.EnvLocalMounts:        "/tmp",
	}, env)
}
