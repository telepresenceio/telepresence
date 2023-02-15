package remotefs

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type ftpMounter struct {
	client rpc.FuseFTPClient
	id     *rpc.MountIdentifier
}

func NewFTPMounter(client rpc.FuseFTPClient) Mounter {
	return &ftpMounter{client: client}
}

func (m *ftpMounter) Start(ctx context.Context, id, clientMountPoint, mountPoint string, podIP net.IP, port uint16) error {
	// The FTPClient and the NewHost must be controlled by the intercept context and not by the pod context that
	// is passed as a parameter, because those services will survive pod changes.
	addr := netip.MustParseAddrPort(iputil.JoinIpPort(podIP, port))
	if m.id == nil {
		dlog.Infof(ctx, "Mounting FTP file system for intercept %q (address %s) at %q", id, addr, clientMountPoint)
		// FTPs remote mount is already relative to the agentconfig.ExportsMountPoint
		rmp := strings.TrimPrefix(mountPoint, agentconfig.ExportsMountPoint)
		id, err := m.client.Mount(ctx, &rpc.MountRequest{
			MountPoint: clientMountPoint,
			FtpServer: &rpc.AddressAndPort{
				Ip:   podIP,
				Port: int32(port),
			},
			ReadTimeout: durationpb.New(5 * time.Second),
			Directory:   rmp,
		})
		if err != nil {
			return err
		}
		m.id = id

		// Ensure unmount when intercept context is cancelled
		go func() {
			<-ctx.Done()
			ctx, cancel := context.WithTimeout(dcontext.WithoutCancel(ctx), time.Second)
			defer cancel()
			if _, err = m.client.Unmount(ctx, m.id); err != nil {
				dlog.Error(ctx, err)
			}
		}()
		return nil
	}

	// Assign a new address to the FTP client. This kills any open connections but leaves the FUSE driver intact
	dlog.Infof(ctx, "Switching remote address to %s for FTP file system for intercept %q at %q", addr, id, clientMountPoint)
	_, err := m.client.SetFtpServer(ctx, &rpc.SetFtpServerRequest{
		FtpServer: &rpc.AddressAndPort{
			Ip:   podIP,
			Port: int32(port),
		},
		Id: m.id,
	})
	return err
}
