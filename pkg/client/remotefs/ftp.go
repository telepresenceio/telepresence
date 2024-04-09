package remotefs

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
	fuseftp "github.com/datawire/go-fuseftp/pkg/fs"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type ftpMounter struct {
	client  fuseftp.FTPClient
	iceptWG *sync.WaitGroup
}

func NewFTPMounter(iceptWG *sync.WaitGroup) Mounter {
	return &ftpMounter{iceptWG: iceptWG}
}

func (m *ftpMounter) Start(ctx context.Context, id, clientMountPoint, mountPoint string, podIP net.IP, port uint16) error {
	// The FTPClient and the NewHost must be controlled by the intercept context and not by the pod context that
	// is passed as a parameter, because those services will survive pod changes.
	addr := netip.MustParseAddrPort(iputil.JoinIpPort(podIP, port))
	if m.client == nil {
		cfg := client.GetConfig(ctx)
		dlog.Infof(ctx, "Mounting FTP file system for intercept %q (address %s) at %q", id, addr, clientMountPoint)
		// FTPs remote mount is already relative to the agentconfig.ExportsMountPoint
		rmp := strings.TrimPrefix(mountPoint, agentconfig.ExportsMountPoint)
		var err error
		m.client, err = fuseftp.NewFTPClient(ctx, netip.MustParseAddrPort(fmt.Sprintf("%s:%d", podIP, port)), rmp, 60*time.Second)
		if err != nil {
			return err
		}
		host := fuseftp.NewHost(m.client, clientMountPoint)
		err = host.Start(ctx, cfg.Timeouts().Get(client.TimeoutFtpReadWrite))
		if err != nil {
			return err
		}

		// Ensure unmount when intercept context is cancelled
		m.iceptWG.Add(1)
		go func() {
			defer m.iceptWG.Done()
			<-ctx.Done()
			dlog.Debugf(ctx, "Unmounting FTP file system for intercept %q (address %s) at %q", id, addr, clientMountPoint)
			host.Stop()
		}()
		return nil
	}

	// Assign a new address to the FTP client. This kills any open connections but leaves the FUSE driver intact
	dlog.Infof(ctx, "Switching remote address to %s for FTP file system for intercept %q at %q", addr, id, clientMountPoint)
	return m.client.SetAddress(netip.MustParseAddrPort(fmt.Sprintf("%s:%d", podIP, port)))
}
