package trafficmgr

import (
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *session) GetConfig() (*client.SessionConfig, error) {
	return &client.SessionConfig{
		ClientFile:   client.GetConfigFile(s),
		LogDirectory: filelocation.AppUserLogDir(s),
		Config:       client.GetConfig(s),
	}, nil
}
