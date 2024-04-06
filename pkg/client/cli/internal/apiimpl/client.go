package apiimpl

import (
	"context"
	"fmt"
	"regexp"

	"github.com/blang/semver/v4"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/api"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type client struct {
	context.Context
}

func (c *client) Connect(cr api.ConnectRequest) (api.Connection, error) {
	dcr := toDaemonRequest(&cr)
	ctx, err := dcr.Commit(c)
	if err != nil {
		return nil, err
	}
	ctx, err = connect.Initializer(ctx)
	if err != nil {
		return nil, err
	}
	return withSession(ctx)
}

func (c *client) Connections() ([]*daemon.Info, error) {
	return daemon.LoadInfos(c)
}

func (c *client) Connection(name string) (api.Connection, error) {
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
	return withSession(daemon.WithRequest(ctx, cr))
}

func withSession(ctx context.Context) (api.Connection, error) {
	ctx, err := connect.EnsureSession(ctx, "", true)
	if err != nil {
		return nil, err
	}
	return &connection{Context: ctx}, nil
}

func (c *client) Helm(hr *helm.Request, cr api.ConnectRequest) error {
	dcr := toDaemonRequest(&cr)
	return hr.Run(c, &dcr.ConnectRequest)
}

func (c *client) Version() semver.Version {
	return version.Structured
}

func NewClient(ctx context.Context) api.Client {
	return &client{Context: ctx}
}
