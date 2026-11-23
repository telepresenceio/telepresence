package session

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// The values that the traffic-manager serves to the client in this suite.
const (
	wshardValidSuffix = "wshard-valid.internal"
	wshardAlsoProxy   = "198.51.100.0/24"

	wshardHostileSshfs     = "/wshard/hostile/sshfs"
	wshardHostileMounts    = "/wshard/hostile/mounts"
	wshardHostileGateway   = "wshard-hostile.example"
	wshardHostileChart     = "oci://wshard-hostile.example/chart"
	wshardHostileAgent     = "wshard-hostile.example/agent:evil"
	wshardHostileCollector = "wshard-hostile.example:443"

	wshardManagerConfigMap = "traffic-manager"
	wshardClientYAMLKey    = "client.yaml"
)

// wshardHostileSuffixes holds one valid DNS suffix and several names that are
// not DNS names but are valid as parts of a file path.
//
//nolint:gochecknoglobals // constant list
var wshardHostileSuffixes = []string{
	wshardValidSuffix,
	"../wshard-escape",
	"x/../../wshard-escape",
	"wshard/slash.internal",
	"wshard bad.internal",
}

// wshardSpec makes the traffic-manager serve the suffixes above and one
// routing setting that the traffic-manager is allowed to decide.
//
//nolint:gochecknoglobals // catalog spec, referenced by both Register and the tests
var wshardSpec = managers.ClientConfig("wshard-hostile", managers.Values{
	Client: managers.Client{
		DNS:     managers.ClientDNS{IncludeSuffixes: wshardHostileSuffixes},
		Routing: managers.ClientRouting{AlsoProxySubnets: []string{wshardAlsoProxy}},
	},
})

// WshardManagerConfig proves that a connecting client does not adopt what the
// traffic-manager serves for settings that name local programs, directories,
// images or charts, nor DNS names that are not valid DNS names, while it does
// adopt the settings the traffic-manager may decide.
type WshardManagerConfig struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardManagerConfig{}, rt.InArea("session"), rt.NeedsManager(wshardSpec))
}

// wshardSessionConfig is the subset of `config view --format json` that this
// suite inspects.
type wshardSessionConfig struct {
	ClientConfig struct {
		Intercept struct {
			SshfsPath  string `json:"sshfsPath"`
			MountsRoot string `json:"mountsRoot"`
		} `json:"intercept"`
		Docker struct {
			HostGateway string `json:"hostGateway"`
		} `json:"docker"`
		Helm struct {
			ChartURL string `json:"chartURL"`
		} `json:"helm"`
		Images struct {
			AgentImage string `json:"agentImage"`
		} `json:"images"`
		Usage struct {
			CollectorAddress string `json:"collectorAddress"`
		} `json:"usage"`
		Routing struct {
			AlsoProxy []string `json:"alsoProxySubnets"`
		} `json:"routing"`
	} `json:"clientConfig"`
}

// wshardQuitOnCleanup stops every daemon when the test ends and makes the
// framework forget its memoized connections.
func wshardQuitOnCleanup(s *rt.Suite) {
	t := s.T()
	t.Cleanup(func() {
		r := rt.R()
		if _, _, err := r.CLI().Run(context.Background(), "quit", "-s"); err != nil {
			r.Infof("[rtest] WshardManagerConfig: quit -s: %v", err)
		}
		r.ForgetConnections()
	})
}

// Test_InvalidDNSNamesAreDropped serves DNS suffixes that are not DNS names
// and asserts that the root daemon's include suffixes keep the valid one and
// none of the others.
func (s *WshardManagerConfig) Test_InvalidDNSNamesAreDropped() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	freeDefaultConnection(t, ns)
	wshardQuitOnCleanup(&s.Suite)
	rt.Reconnect(t, ctx, ns)

	var st daemonStatus
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))
	s.Contains(st.RootDaemon.DNS.IncludeSuffixes, wshardValidSuffix, "the valid suffix must survive")
	for _, name := range wshardHostileSuffixes[1:] {
		s.NotContains(st.RootDaemon.DNS.IncludeSuffixes, name, "invalid DNS name %q must be dropped", name)
	}
}

// Test_LocalSettingsAreNotAdopted serves values for the settings that name
// local programs, directories, images, charts and the usage collector, and
// asserts that the session's effective configuration keeps its own values
// while it adopts a routing setting served in the same configuration.
func (s *WshardManagerConfig) Test_LocalSettingsAreNotAdopted() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	r := s.R()
	mgrNS := managers.ManagerNamespace

	// The test patches the shared release, so the fixture is re-provisioned after it.
	rt.Mutate(t, rt.ManagerFixture(wshardSpec))

	original, err := r.Kubectl(ctx, mgrNS, "get", "configmap", wshardManagerConfigMap,
		"-o", "jsonpath={.data.client\\.yaml}")
	s.Require().NoError(err)
	var doc map[string]any
	s.Require().NoError(yaml.Unmarshal([]byte(original), &doc))
	if doc == nil {
		doc = map[string]any{}
	}
	doc["intercept"] = map[string]any{"sshfsPath": wshardHostileSshfs, "mountsRoot": wshardHostileMounts}
	doc["docker"] = map[string]any{"hostGateway": wshardHostileGateway}
	doc["helm"] = map[string]any{"chartURL": wshardHostileChart}
	doc["images"] = map[string]any{"agentImage": wshardHostileAgent}
	doc["usage"] = map[string]any{"collectorAddress": wshardHostileCollector}

	patchConfigMap := func(content string) error {
		patch, err := json.Marshal(map[string]any{"data": map[string]string{wshardClientYAMLKey: content}})
		if err != nil {
			return err
		}
		_, err = r.Kubectl(context.Background(), mgrNS, "patch", "configmap", wshardManagerConfigMap,
			"--type", "merge", "-p", string(patch))
		return err
	}
	t.Cleanup(func() {
		if err := patchConfigMap(original); err != nil {
			t.Errorf("restoring %s: %v", wshardManagerConfigMap, err)
		}
	})
	hostile, err := yaml.Marshal(doc)
	s.Require().NoError(err)
	s.Require().NoError(patchConfigMap(string(hostile)))

	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, mgrNS)
	s.Require().NoError(err)
	defer closeFn()
	s.Require().Eventually(func() bool {
		cc, err := mc.GetClientConfig(ctx, &emptypb.Empty{})
		return err == nil && strings.Contains(string(cc.GetConfigYaml()), wshardHostileSshfs)
	}, 30*time.Second, time.Second, "the traffic-manager must serve the hostile settings")

	freeDefaultConnection(t, ns)
	wshardQuitOnCleanup(&s.Suite)
	rt.Reconnect(t, ctx, ns)

	var cfg wshardSessionConfig
	s.Require().NoError(s.CLI().JSON(ctx, &cfg, "config", "view", "--format", "json"))
	cc := cfg.ClientConfig
	s.Contains(cc.Routing.AlsoProxy, wshardAlsoProxy, "a setting the traffic-manager may decide must be adopted")
	s.NotEqual(wshardHostileSshfs, cc.Intercept.SshfsPath, "intercept.sshfsPath")
	s.NotEqual(wshardHostileMounts, cc.Intercept.MountsRoot, "intercept.mountsRoot")
	s.NotEqual(wshardHostileGateway, cc.Docker.HostGateway, "docker.hostGateway")
	s.NotEqual(wshardHostileChart, cc.Helm.ChartURL, "helm.chartURL")
	s.NotEqual(wshardHostileAgent, cc.Images.AgentImage, "images.agentImage")
	s.NotEqual(wshardHostileCollector, cc.Usage.CollectorAddress, "usage.collectorAddress")
}
