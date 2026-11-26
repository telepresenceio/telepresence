package connect

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

type PortForwardRecovery struct{ rt.Suite }

func init() {
	rt.Register(&PortForwardRecovery{}, rt.InArea("connect"), rt.NeedsManager(managers.Default))
}

func (s *PortForwardRecovery) Test_FirstStreamFailureRecovers() {
	wl := s.Workload(workloads.Echo("portforward-recovery"))
	ctx, cancel := context.WithTimeout(s.Ctx(), time.Minute)
	defer cancel()
	rawConfig, err := s.R().Kubectl(ctx, "", "config", "view", "--raw", "--minify", "--flatten", "-o", "json")
	s.Require().NoError(err)
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(rawConfig))
	s.Require().NoError(err)
	cfg.NextProtos = []string{"http/1.1"}
	api, err := kubernetes.NewForConfig(cfg)
	s.Require().NoError(err)
	ctx = k8sapi.WithK8sInterface(ctx, api)
	pap, err := portforward.ResolveSvcToPod(ctx, wl.SvcName, wl.Namespace, strconv.Itoa(wl.Port))
	s.Require().NoError(err)
	target, err := url.Parse(cfg.Host)
	s.Require().NoError(err)
	transport, err := rest.TransportFor(cfg)
	s.Require().NoError(err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	var upgrades atomic.Int32
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode == http.StatusSwitchingProtocols && upgrades.Add(1) == 1 {
			// Close the real API-server transport after its upgrade, before
			// the client can open its first port-forward stream.
			return resp.Body.Close()
		}
		return nil
	}
	server := httptest.NewServer(proxy)
	defer server.Close()
	config := client.GetDefaultConfig()
	config.Cluster().ForceSPDY = true
	ctx = client.WithConfig(ctx, config)
	ctx = portforward.WithRestConfig(ctx, &rest.Config{Host: server.URL})
	dial := portforward.Dialer(ctx)
	conn, err := dial(ctx, pap.String())
	if conn != nil {
		_ = conn.Close()
	}
	s.Require().ErrorContains(err, "error creating error stream")
	s.Require().Equal(int32(1), upgrades.Load())

	// The next request uses the same cache and pod identity. It must open
	// a fresh API-server transport and reach the real echo pod.
	ht := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dial(ctx, pap.String())
	}}
	defer ht.CloseIdleConnections()
	hc := &http.Client{Transport: ht, Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wl.ServiceURL(), nil)
	s.Require().NoError(err)
	resp, err := hc.Do(req)
	s.Equal(int32(2), upgrades.Load(), "recovery must open a fresh transport")
	s.Require().NoError(err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, resp.StatusCode)
	s.Contains(string(body), wl.Name)
}
