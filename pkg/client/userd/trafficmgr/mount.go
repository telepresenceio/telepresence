package trafficmgr

import (
	"context"
	"net/netip"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func (pa *podAccess) shouldMount() bool {
	return (pa.ftpPort > 0 || pa.sftpPort > 0) && (pa.localMountPort > 0 || pa.clientMountPoint != "")
}

// startMount starts the mount for the given podAccessKey.
// It assumes that the user has called shouldMount and is sure that something will be started.
func (pa *podAccess) startMount(ctx context.Context, iceptWG, podWG *sync.WaitGroup) {
	var fuseftp rpc.FuseFTPClient
	useFtp := client.GetConfig(ctx).Intercept().UseFtp
	addr, err := netip.ParseAddr(pa.podIP)
	if err != nil {
		dlog.Errorf(ctx, "error parsing pod IP address %q: %v", pa.podIP, err)
		return
	}
	var port uint16
	mountCtx := ctx
	if useFtp {
		if pa.ftpPort == 0 {
			dlog.Error(ctx, "Client is configured to perform remote mounts using FTP, but only SFTP is provided by the traffic-agent")
			return
		}
		if pa.localMountPort > 0 {
			dlog.Error(ctx, "Client is configured to perform remote mounts using FTP, but only SFTP can be used with --local-mount-port")
			return
		}
		// The FTP mounter survives multiple starts for the same intercept. It just resets the address
		mountCtx = pa.ctx
		fuseftp = getSession(ctx).GetService().FuseFTPMgr().GetFuseFTPClient(ctx)
		if fuseftp == nil {
			dlog.Error(ctx, "Client is configured to perform remote mounts using FTP, but the fuseftp server was unable to start")
			return
		}
		port = uint16(pa.ftpPort)
	} else {
		if pa.sftpPort == 0 {
			dlog.Error(ctx, "Client is configured to perform remote mounts using SFTP, but only FTP is provided by the traffic-agent")
			return
		}
		port = uint16(pa.sftpPort)
	}

	m := *pa.mounter
	if m == nil {
		switch {
		case pa.localMountPort != 0:
			session := getSession(ctx)
			m = remotefs.NewBridgeMounter(tunnel.SessionID(session.SessionInfo().SessionId), session.ManagerClient(), uint16(pa.localMountPort))
		case useFtp:
			m = remotefs.NewFTPMounter(fuseftp, iceptWG)
		default:
			m = remotefs.NewSFTPMounter(iceptWG, podWG)
		}
		*pa.mounter = m
	}
	err = m.Start(mountCtx, pa.workload, pa.container, pa.clientMountPoint, pa.mountPoint, netip.AddrPortFrom(addr, port), pa.readOnly)
	if err != nil && ctx.Err() == nil {
		dlog.Error(ctx, err)
	}
}

func (s *session) ensureNoMountConflict(localMountPoint string, localMountPort int32) (err error) {
	if localMountPoint == "" && localMountPort == 0 {
		return nil
	}
	s.currentInterceptsLock.Lock()
	for _, ic := range s.currentIntercepts {
		if localMountPoint != "" && ic.ClientMountPoint == localMountPoint {
			err = status.Errorf(codes.AlreadyExists, "mount point %s already in use by intercept %s", localMountPoint, ic.Spec.Name)
			break
		}
		if localMountPort != 0 && ic.localMountPort == localMountPort {
			err = status.Errorf(codes.AlreadyExists, "mount port %d already in use by intercept %s", localMountPort, ic.Spec.Name)
			break
		}
	}
	s.currentInterceptsLock.Unlock()
	if err != nil {
		return err
	}

	s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
		if localMountPoint != "" && ig.localMountPoint == localMountPoint {
			err = status.Errorf(codes.AlreadyExists, "mount point %s already in use by ingest %s", localMountPoint, key)
			return false
		}
		if localMountPort != 0 && ig.localMountPort == localMountPort {
			err = status.Errorf(codes.AlreadyExists, "mount port %d already in use by ingest %s", localMountPort, key)
			return false
		}
		return true
	})
	return err
}
