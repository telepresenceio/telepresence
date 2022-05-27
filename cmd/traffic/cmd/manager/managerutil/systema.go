package managerutil

import (
	"context"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	systemarpc "github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type SystemaCRUDClient interface {
	systemarpc.SystemACRUDClient
	a8rcloud.Closeable
}

func GetAgentImage(ctx context.Context) string {
	var agentImage string
	env := GetEnv(ctx)

	// The AgentImage in the container's environment have the highest priority. If it isn't provided,
	// then we as SystemA for the preferred image. This means that air-gapped environment can declare
	// their preferred image using the environment variable and refrain from an attempt to contact
	// SystemA.
	if env.AgentImage == "" {
		var err error
		if agentImage, err = AgentImageFromSystemA(ctx); err != nil {
			dlog.Errorf(ctx, "unable to get Ambassador Cloud preferred agent image: %v", err)
		}
	}
	if agentImage == "" {
		agentImage = env.QualifiedAgentImage()
	}
	return agentImage
}

func AgentImageFromSystemA(ctx context.Context) (string, error) {
	systemaPool := a8rcloud.GetSystemAPool[SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName)
	systemaClient, err := systemaPool.Get(ctx)
	if err != nil {
		return "", err
	}
	resp, err := systemaClient.PreferredAgent(ctx, &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	})
	if err != nil {
		return "", err
	}
	if err = systemaPool.Done(ctx); err != nil {
		return "", err
	}
	return resp.GetImageName(), nil
}
