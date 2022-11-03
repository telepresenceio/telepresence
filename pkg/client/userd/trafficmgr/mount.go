package trafficmgr

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dpipe"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func (ic *intercept) shouldMount() bool {
	return (ic.FtpPort > 0 || ic.SftpPort > 0) && ic.ClientMountPoint != ""
}

// startMount starts the mount for the given podInterceptKey.
// It assumes that the user has called shouldMount and is sure that something will be started.
func (ic *intercept) startMount(ctx context.Context, fuseftp rpc.FuseFTPClient, podWG *sync.WaitGroup) {
	useFtp := client.GetConfig(ctx).Intercept.UseFtp
	switch {
	case ic.FtpPort == 0 && useFtp:
		dlog.Errorf(ctx, "Client is configured to perform remote mounts using FTP, but only SFTP is provided by the traffic-agent")
		return
	case fuseftp == nil && useFtp:
		dlog.Errorf(ctx, "Client is configured to perform remote mounts using FTP, but the fuseftp server was unable to start")
		return
	case ic.SftpPort == 0 && !useFtp:
		dlog.Errorf(ctx, "Client is configured to perform remote mounts using SFTP, but only FTP is provided by the traffic-agent")
		return
	}

	m := ic.mounter
	if m == nil {
		if useFtp {
			m = &ftpMounter{client: fuseftp}
		} else {
			m = &sftpMounter{podWG: podWG}
		}
		ic.mounter = m
	}
	err := m.start(ctx, ic)
	if err != nil && ctx.Err() == nil {
		dlog.Error(ctx, err)
	}
}

type ftpMounter struct {
	client rpc.FuseFTPClient
	id     *rpc.MountIdentifier
}

func (m *ftpMounter) start(_ context.Context, ic *intercept) error {
	// The FTPClient and the NewHost must be controlled by the intercept context and not by the pod context that
	// is passed as a parameter, because those services will survive pod changes.
	ctx := ic.ctx
	addr := netip.MustParseAddrPort(fmt.Sprintf("%s:%d", ic.PodIp, ic.FtpPort))
	if m.id == nil {
		dlog.Infof(ctx, "Mounting FTP file system for intercept %q (address %s) at %q", ic.Id, addr, ic.ClientMountPoint)
		// FTPs remote mount is already relative to the agentconfig.ExportsMountPoint
		rmp := strings.TrimPrefix(ic.MountPoint, agentconfig.ExportsMountPoint)
		id, err := m.client.Mount(ctx, &rpc.MountRequest{
			MountPoint: ic.ClientMountPoint,
			FtpServer: &rpc.AddressAndPort{
				Ip:   iputil.Parse(ic.PodIp),
				Port: ic.FtpPort,
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
			<-ic.ctx.Done()
			ctx, cancel := context.WithTimeout(dcontext.WithoutCancel(ctx), time.Second)
			defer cancel()
			if _, err = m.client.Unmount(ctx, m.id); err != nil {
				dlog.Error(ctx, err)
			}
		}()
		return nil
	}

	// Assign a new address to the FTP client. This kills any open connections but leaves the FUSE driver intact
	dlog.Infof(ctx, "Switching remote address to %s for FTP file system for intercept %q at %q", addr, ic.Id, ic.ClientMountPoint)
	_, err := m.client.SetFtpServer(ctx, &rpc.SetFtpServerRequest{
		FtpServer: &rpc.AddressAndPort{
			Ip:   iputil.Parse(ic.PodIp),
			Port: ic.FtpPort,
		},
		Id: m.id,
	})
	return err
}

type sftpMounter struct {
	sync.Mutex
	podWG *sync.WaitGroup
}

func (m *sftpMounter) start(ctx context.Context, ic *intercept) error {
	ctx = dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s:%d", ic.PodIp, ic.SftpPort))

	// The mount is terminated and restarted when the intercept pod changes, so we
	// must set up a wait/done pair here to ensure that this happens synchronously
	m.podWG.Add(1)
	go func() {
		defer m.podWG.Done()

		// Be really sure that the following doesn't happen in parallel using multiple
		// pods for the same intercept. One must die before the next is created.
		m.Lock()
		defer m.Unlock()

		dlog.Infof(ctx, "Mounting SFTP file system for intercept %q (pod %s) at %q", ic.Id, ic.PodIp, ic.ClientMountPoint)
		defer dlog.Infof(ctx, "Unmounting SFTP file system for intercept %q (pod %s) at %q", ic.Id, ic.PodIp, ic.ClientMountPoint)

		// Retry mount in case it gets disconnected
		err := client.Retry(ctx, "sshfs", func(ctx context.Context) error {
			dl := &net.Dialer{Timeout: 3 * time.Second}
			conn, err := dl.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", ic.PodIp, ic.SftpPort))
			if err != nil {
				return err
			}
			defer conn.Close()
			sshfsArgs := []string{
				"-F", "none", // don't load the user's config file
				"-f", // foreground operation

				// connection settings
				"-C", // compression
				"-oConnectTimeout=10",
				"-oStrictHostKeyChecking=no",     // don't bother checking the host key...
				"-oUserKnownHostsFile=/dev/null", // and since we're not checking it, don't bother remembering it either
				"-o", "slave",                    // Unencrypted via stdin/stdout

				// mount directives
				"-o", "follow_symlinks",
				"-o", "allow_root", // needed to make --docker-run work as docker runs as root
				"localhost:" + ic.MountPoint, // what to mount
				ic.ClientMountPoint,          // where to mount it
			}
			exe := "sshfs"
			if runtime.GOOS == "windows" {
				// Use sshfs-win to launch the sshfs
				sshfsArgs = append([]string{"cmd", "-ouid=-1", "-ogid=-1"}, sshfsArgs...)
				exe = "sshfs-win"
			}
			err = dpipe.DPipe(ctx, conn, exe, sshfsArgs...)
			time.Sleep(time.Second)

			// sshfs sometimes leave the mount point in a bad state. This will clean it up
			ctx, cancel := context.WithTimeout(dcontext.WithoutCancel(ctx), time.Second)
			defer cancel()
			_ = proc.CommandContext(ctx, "fusermount", "-uz", ic.ClientMountPoint).Run()
			return err
		}, 3*time.Second, 6*time.Second)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}()
	return nil
}
