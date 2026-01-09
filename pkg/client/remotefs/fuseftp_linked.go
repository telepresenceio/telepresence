//go:build linked_fuseftp && !docker

package remotefs

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/go-fuseftp/pkg/fs"
	"github.com/telepresenceio/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type ftpMounter struct {
	mountPoint string
	cancel     context.CancelFunc
	ftpClient  fs.FTPClient
	iceptWG    *sync.WaitGroup
}

type fuseFtpMgr struct{}

func (s *fuseFtpMgr) LinkedFTP() bool {
	return true
}

func NewFuseFTPManager() FuseFTPManager {
	return &fuseFtpMgr{}
}

func (s *fuseFtpMgr) DeferInit(_ context.Context) error {
	return nil
}

func (s *fuseFtpMgr) GetFuseFTPClient(_ context.Context) rpc.FuseFTPClient {
	return rpc.NewFuseFTPClient(nil)
}

func NewFTPMounter(_ rpc.FuseFTPClient, iceptWG *sync.WaitGroup) Mounter {
	return &ftpMounter{iceptWG: iceptWG}
}

func (m *ftpMounter) Start(ctx context.Context, workload, container, clientMountPoint, mountPoint string, podAddrPort netip.AddrPort, ro bool) error {
	roTxt := ""
	if ro {
		roTxt = " read-only"
	}
	if m.ftpClient == nil {
		cfg := client.GetConfig(ctx)
		clog.Infof(ctx, "Mounting FTP file system for container %s[%s] (address %s)%s at %q", workload, container, podAddrPort, roTxt, clientMountPoint)
		// FTPs remote mount is already relative to the agentconfig.ExportsMountPoint
		rmp := strings.TrimPrefix(mountPoint, agentconfig.ExportsMountPoint)
		ftpClient, err := fs.NewFTPClient(ctx.Done(), podAddrPort, rmp, ro, cfg.Timeouts().Get(client.TimeoutFtpReadWrite))
		if err != nil {
			return err
		}
		host := fs.NewHost(ftpClient, clientMountPoint)
		if err = host.Start(ctx, 5*time.Second); err != nil {
			return err
		}

		m.ftpClient = ftpClient
		// Ensure unmount when intercept context is cancelled
		m.iceptWG.Add(1)
		go func() {
			defer m.iceptWG.Done()
			<-ctx.Done()
			clog.Debugf(ctx, "Unmounting FTP file system for container %s[%s] (address %s) at %q", workload, container, podAddrPort, clientMountPoint)
		}()
		clog.Infof(ctx, "File system for container %s[%s] (address %s) successfully mounted%s at %q", workload, container, podAddrPort, roTxt, clientMountPoint)
		return nil
	}

	// Assign a new address to the FTP client. This kills any open connections but leaves the FUSE driver intact
	clog.Infof(ctx, "Switching remote address to %s for FTP file system for workload container %s[%s] at %q", podAddrPort, workload, container, clientMountPoint)
	return m.ftpClient.SetAddress(podAddrPort)
}
