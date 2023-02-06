package trafficmgr

import (
	"context"
	"sync"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
)

func (ic *intercept) shouldMount() bool {
	return (ic.FtpPort > 0 || ic.SftpPort > 0) && ic.ClientMountPoint != ""
}

// startMount starts the mount for the given podInterceptKey.
// It assumes that the user has called shouldMount and is sure that something will be started.
func (ic *intercept) startMount(ctx context.Context, podWG *sync.WaitGroup) {
	var fuseftp rpc.FuseFTPClient
	useFtp := client.GetConfig(ctx).Intercept.UseFtp
	var port int32
	mountCtx := ctx
	if useFtp {
		if ic.FtpPort == 0 {
			dlog.Errorf(ctx, "Client is configured to perform remote mounts using FTP, but only SFTP is provided by the traffic-agent")
			return
		}
		if fuseftp = userd.GetService(ctx).FuseFTPMgr().GetFuseFTPClient(ctx); fuseftp == nil {
			dlog.Errorf(ctx, "Client is configured to perform remote mounts using FTP, but the fuseftp server was unable to start")
			return
		}
		port = ic.FtpPort

		// The FTP mounter survives multiple starts for the same intercept. It just resets the port
		mountCtx = ic.ctx
	} else {
		if ic.SftpPort == 0 {
			dlog.Errorf(ctx, "Client is configured to perform remote mounts using SFTP, but only FTP is provided by the traffic-agent")
			return
		}
		port = ic.SftpPort
	}

	m := ic.Mounter
	if m == nil {
		if useFtp {
			m = remotefs.NewFTPMounter(fuseftp)
		} else {
			m = remotefs.NewSFTPMounter(podWG)
		}
		ic.Mounter = m
	}
	err := m.Start(mountCtx, ic.Id, ic.ClientMountPoint, ic.MountPoint, ic.PodIp, port)
	if err != nil && ctx.Err() == nil {
		dlog.Error(ctx, err)
	}
}
