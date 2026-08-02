//go:build !docker

package remotefs

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/go-fuseftp/pkg/fs"
	"github.com/telepresenceio/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type ftpMounter struct {
	ftpClient fs.FTPClient
	iceptWG   *sync.WaitGroup
	// tokenProvider yields the current session credential token to use as the FTP
	// password, or "" to log in anonymously. Read once, when ftpClient is created.
	tokenProvider func() string
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

func NewFTPMounter(_ rpc.FuseFTPClient, iceptWG *sync.WaitGroup, tokenProvider func() string) Mounter {
	return &ftpMounter{iceptWG: iceptWG, tokenProvider: tokenProvider}
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

		// The dial may lose a race against the routing of a just-created pod's IP, so
		// dial errors are retried until the intercept timeout expires.
		var ftpClient fs.FTPClient
		bc := backoff.NewExponentialBackOff()
		bc.InitialInterval = 100 * time.Millisecond
		bc.MaxInterval = 3 * time.Second
		bc.MaxElapsedTime = cfg.Timeouts().Get(client.TimeoutIntercept)
		var token string
		if m.tokenProvider != nil {
			token = m.tokenProvider()
		}
		err := backoff.Retry(func() error {
			var err error
			readTimeout := cfg.Timeouts().Get(client.TimeoutFtpReadWrite)
			if token != "" {
				// Username is deliberately "anonymous": an old agent's FTP server only
				// knows that user, and its wildcard password accepts the token, so this
				// keeps working against old agents too.
				ftpClient, err = fs.NewFTPClientWithAuth(ctx.Done(), podAddrPort, rmp, ro, readTimeout, "anonymous", token)
			} else {
				ftpClient, err = fs.NewFTPClient(ctx.Done(), podAddrPort, rmp, ro, readTimeout)
			}
			if err != nil {
				clog.Debugf(ctx, "FTP connection to %s failed (%v), retrying", podAddrPort, err)
			}
			return err
		}, backoff.WithContext(bc, ctx))
		if err != nil {
			return err
		}
		host := fs.NewHost(ftpClient, clientMountPoint)
		if err := host.Start(ctx, 5*time.Second); err != nil {
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
