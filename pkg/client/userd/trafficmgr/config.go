package trafficmgr

import (
	"path/filepath"

	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
)

func (s *session) GetConfig() (*client.SessionConfig, error) {
	ctx := s.context
	nc, err := s.rootDaemon.GetNetworkConfig(ctx, &empty.Empty{})
	if err != nil {
		return nil, err
	}
	rc := client.GetDefaultConfig()
	err = json.Unmarshal(nc.ClientConfig, rc, true)
	if err != nil {
		return nil, err
	}
	return &client.SessionConfig{
		ClientFile:   filepath.Join(filelocation.AppUserConfigDir(ctx), client.ConfigFile),
		LogDirectory: filelocation.AppUserLogDir(ctx),
		Config:       client.GetConfig(ctx).Merge(rc),
	}, nil
}
