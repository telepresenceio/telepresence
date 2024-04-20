package apiimpl

import (
	"context"
	"fmt"
	"regexp"

	"github.com/blang/semver/v4"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/api"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type impl struct {
	context.Context
}

func (c *impl) Connect(cr api.ConnectRequest) (api.Connection, error) {
	dcr := toDaemonRequest(&cr)
	ctx, err := dcr.Commit(c)
	if err != nil {
		return nil, err
	}
	if cr.Stdout != nil {
		ctx = dos.WithStdout(ctx, cr.Stdout)
	}
	ctx, err = connect.Initializer(ctx)
	if err != nil {
		return nil, err
	}
	if cr.Docker {
		ctx = docker.EnableClient(ctx)
	}
	return withSession(ctx)
}

func (c *impl) QuitAllDaemons() {
	connect.Quit(c)
}

func (c *impl) Connections() ([]*daemon.Info, error) {
	return daemon.LoadInfos(c)
}

func (c *impl) Connection(name string) (api.Connection, error) {
	ci, err := daemon.LoadMatchingInfo(c, regexp.MustCompile(fmt.Sprintf(`\A%s\z`, regexp.QuoteMeta(name))))
	if err != nil {
		return nil, err
	}
	ud, err := connect.ExistingDaemon(c, ci)
	if err != nil {
		return nil, err
	}
	ctx := daemon.WithUserClient(c, ud)
	cr := daemon.NewDefaultRequest()
	cr.Implicit = true
	cr.Docker = ud.Containerized()
	if cr.Docker {
		ctx = docker.EnableClient(ctx)
	}
	return withSession(daemon.WithRequest(ctx, cr))
}

func withSession(ctx context.Context) (api.Connection, error) {
	ctx, err := connect.EnsureSession(ctx, "", true)
	if err != nil {
		return nil, err
	}
	return &connection{Context: ctx}, nil
}

func (c *impl) Helm(hr *helm.Request, cr api.ConnectRequest) error {
	dcr := toDaemonRequest(&cr)
	return hr.Run(c, &dcr.ConnectRequest)
}

func (c *impl) Version() semver.Version {
	return version.Structured
}

func NewClient(ctx context.Context) api.Client {
	env, err := client.LoadEnv()
	if err != nil {
		panic(err)
	}
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		panic(fmt.Errorf("failed to load config: %w", err))
	}
	ctx = client.WithConfig(ctx, cfg)
	if ctx, err = logging.InitContext(ctx, "cli", logging.RotateDaily, false); err != nil {
		panic(err)
	}
	return &impl{Context: client.WithEnv(ctx, env)}
}
