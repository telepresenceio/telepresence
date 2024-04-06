package integration_test

import (
	"context"
	goRuntime "runtime"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"helm.sh/helm/v3/pkg/cli/values"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/api"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type apiSuite struct {
	itest.Suite
	itest.NamespacePair
	svc string
}

func (s *apiSuite) SuiteName() string {
	return "API"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &apiSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h, svc: "echo"}
	})
}

func (s *apiSuite) request() api.ConnectRequest {
	rq := api.ConnectRequest{}
	rq.KubeFlags = map[string]string{
		"namespace":  s.AppNamespace(),
		"kubeconfig": itest.KubeConfig(s.Context()),
	}
	rq.ManagerNamespace = s.ManagerNamespace()
	return rq
}

func (s *apiSuite) AmendSuiteContext(ctx context.Context) context.Context {
	// The default executable will be the test executable. We need the telepresence executable here.
	return dos.WithExe(ctx, s)
}

func (s *apiSuite) SetupSuite() {
	s.Suite.SetupSuite()
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx := s.Context()
		hr := helm.Request{
			Options: values.Options{
				Values: s.GetValuesForHelm(ctx, nil, false),
			},
			Type: helm.Install,
		}
		s.Require().NoError(cli.NewClient(ctx).Helm(&hr, s.request()))
	}()
	go func() {
		defer wg.Done()
		s.ApplyEchoService(s.Context(), s.svc, 80)
	}()
	wg.Wait()
}

func (s *apiSuite) TearDownSuite() {
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.DeleteSvcAndWorkload(s.Context(), "deploy", s.svc)
	}()
	go func() {
		defer wg.Done()
		hr := helm.Request{Type: helm.Uninstall}
		s.Require().NoError(cli.NewClient(s.Context()).Helm(&hr, s.request()))
	}()
	wg.Wait()
}

func (s *apiSuite) Test_Connect() {
	rq := s.Require()
	ctx := s.Context()
	client := cli.NewClient(ctx)
	conn, err := client.Connect(s.request())
	rq.NoError(err)
	defer func() {
		s.NoError(conn.Close())
	}()
	rq.Equal(conn.Info().Namespace, s.AppNamespace())
}

func (s *apiSuite) Test_List() {
	rq := s.Require()
	ctx := s.Context()
	client := cli.NewClient(ctx)

	conn, err := client.Connect(s.request())
	rq.NoError(err)
	defer func() {
		s.NoError(conn.Close())
	}()

	wfs, err := conn.List("")
	rq.NoError(err)
	rq.Len(wfs, 1)
	rq.Equal(wfs[0].Name, s.svc)
	rq.Equal(conn.Info().Namespace, s.AppNamespace())
}

func (s *apiSuite) Test_StartIntercept() {
	rq := s.Require()
	ctx := s.Context()
	client := cli.NewClient(ctx)

	conn, err := client.Connect(s.request())
	rq.NoError(err)
	defer func() {
		s.NoError(conn.Close())
	}()

	ii, err := conn.StartIntercept(api.InterceptRequest{
		WorkloadName: "echo",
		Port:         "8080",
	}, "")
	rq.NoError(err)
	defer func() {
		s.NoError(conn.EndIntercept(ii.Name))
	}()
	s.NotNil(ii.Environment)
	s.Equal("echo-server", ii.Environment["TELEPRESENCE_CONTAINER"])
}

func (s *apiSuite) Test_RunIntercept() {
	if s.IsCI() && !(goRuntime.GOOS == "linux" && goRuntime.GOARCH == "amd64") {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
	}

	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Intercept().UseFtp = false
	})

	rq := s.Require()
	client := cli.NewClient(ctx)

	cr := s.request()
	cr.Docker = true
	conn, err := client.Connect(cr)
	rq.NoError(err)
	defer func() {
		s.NoError(conn.Close())
	}()

	_, err = conn.RunIntercept(
		api.InterceptRequest{
			WorkloadName: "echo",
			Port:         "8080",
		},
		api.DockerRunInterceptHandler{
			Image:     "busybox",
			Arguments: []string{"ls", "/var/run/secrets/kubernetes.io/serviceaccount"},
		})
	rq.NoError(err)
}

func (s *apiSuite) Test_Connections() {
	if s.IsCI() && !(goRuntime.GOOS == "linux" && goRuntime.GOARCH == "amd64") {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
	}

	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Intercept().UseFtp = false
	})

	// Establish two connections
	rq := s.Require()
	client := cli.NewClient(ctx)
	r1 := s.request()
	r1.Name = "alpha"
	r1.Docker = true
	c1, err := client.Connect(r1)
	rq.NoError(err)
	defer func() {
		s.NoError(c1.Close())
	}()

	r2 := s.request()
	r2.Name = "beta"
	r2.Docker = true
	r2.KubeFlags["namespace"] = s.ManagerNamespace()
	c2, err := client.Connect(r2)
	rq.NoError(err)
	defer func() {
		s.NoError(c2.Close())
	}()

	// Verify that the two connections can be listed
	conns, err := client.Connections()
	rq.NoError(err)
	rq.Equal(2, len(conns))

	if conns[0].Name == r1.Name {
		rq.Equal(conns[1].Name, r2.Name)
	} else {
		rq.Equal(conns[1].Name, r1.Name)
		rq.Equal(conns[0].Name, r2.Name)
	}

	// Retrieve connection by name
	cc1, err := client.Connection("alpha")
	rq.NoError(err)

	// The error is set to ALREADY_CONNECTED when the connection is already established. This is expected.
	ignoreErr := cmpopts.IgnoreFields(connector.ConnectInfo{}, "Error")
	ignoreUnEx := cmp.FilterPath(unexported, cmp.Ignore())

	rq.True(cmp.Equal(c1.Info(), cc1.Info(), ignoreErr, ignoreUnEx), cmp.Diff(c1.Info(), cc1.Info(), ignoreErr, ignoreUnEx))

	// Retrieve connection by name
	cc2, err := client.Connection("beta")
	rq.NoError(err)
	rq.True(cmp.Equal(c2.Info(), cc2.Info(), ignoreErr, ignoreUnEx), cmp.Diff(c2.Info(), cc2.Info(), ignoreErr, ignoreUnEx))
}

func (s *apiSuite) Test_Version() {
	client := cli.NewClient(s.Context())
	s.Equal(version.Structured, client.Version())
}

func unexported(p cmp.Path) bool {
	if f, ok := p.Index(-1).(cmp.StructField); ok {
		r, _ := utf8.DecodeRuneInString(f.Name())
		return !unicode.IsUpper(r)
	}
	return false
}
