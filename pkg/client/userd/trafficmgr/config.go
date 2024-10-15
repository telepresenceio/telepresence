package trafficmgr

import (
	"context"
	"path/filepath"

	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *session) GetConfig(ctx context.Context) (*client.SessionConfig, error) {
	nc, err := s.rootDaemon.GetNetworkConfig(ctx, &empty.Empty{})
	if err != nil {
		return nil, err
	}
	rc := client.GetDefaultConfig()
	err = client.UnmarshalJSON(nc.ClientConfig, rc, true)
	if err != nil {
		return nil, err
	}
	return &client.SessionConfig{
		ClientFile: filepath.Join(filelocation.AppUserConfigDir(ctx), client.ConfigFile),
		Config:     client.GetConfig(ctx).Merge(rc),
	}, nil
}
