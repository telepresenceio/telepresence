package remotefs

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dpipe"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type sftpMounter struct {
	sync.Mutex
	iceptWG *sync.WaitGroup
	podWG   *sync.WaitGroup
}

func NewSFTPMounter(iceptWG, podWG *sync.WaitGroup) Mounter {
	return &sftpMounter{iceptWG: iceptWG, podWG: podWG}
}

func (m *sftpMounter) Start(ctx context.Context, workload, container, clientMountPoint, mountPoint string, podAddrPort netip.AddrPort, ro bool) error {
	ctx = clog.WithGroup(ctx, podAddrPort.String())
	podIP := podAddrPort.Addr().Unmap()

	// The mount is terminated and restarted when the intercept pod changes, so we
	// must set up a wait/done pair here to ensure that this happens synchronously
	m.podWG.Add(1)
	m.iceptWG.Add(1)
	go func() {
		defer m.iceptWG.Done()
		defer m.podWG.Done()

		// Be really sure that the following doesn't happen in parallel using multiple
		// pods for the same intercept. One must die before the next is created.
		m.Lock()
		defer m.Unlock()

		clog.Infof(ctx, "Mounting SFTP file system for container %s[%s] (pod %s) at %q", workload, container, podIP, clientMountPoint)
		if runtime.GOOS != "windows" {
			defer func() {
				clog.Infof(ctx, "Unmounting SFTP file system for container %s[%s] (pod %s) at %q", workload, container, podIP, clientMountPoint)
				time.Sleep(time.Second)

				// sshfs sometimes leave the mount point in a bad state. This will clean it up
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				defer cancel()
				var umount *exec.Cmd
				if runtime.GOOS == "darwin" {
					umount = proc.CommandContext(ctx, "umount", "-f", clientMountPoint)
				} else {
					umount = proc.CommandContext(ctx, "fusermount", "-uz", clientMountPoint)
				}
				_ = umount.Run()
			}()
		}

		// Retry mount in case it gets disconnected
		bc := backoff.WithContext(backoff.NewConstantBackOff(3*time.Second), ctx)
		err := backoff.Retry(func() error {
			sshfsArgs := []string{
				"-F", "none", // don't load the user's config file
				"-f", // foreground operation

				// connection settings
				"-C", // compression
				"-oConnectTimeout=10",

				// mount directives
				"-o", "follow_symlinks",
			}

			useFsKit := runtime.GOOS == "darwin" && client.GetConfig(ctx).Intercept().UseMacosFsKit
			if !useFsKit {
				// allow_root is a kernel mount option not supported by the FSKit backend
				sshfsArgs = append(sshfsArgs, "-o", "allow_root")
			} else {
				sshfsArgs = append(sshfsArgs, "-o", "backend=fskit")
			}

			if ro {
				sshfsArgs = append(sshfsArgs, "-o", "ro")
			}

			useIPv6 := podIP.Is6()
			if useIPv6 {
				// Must use stdin/stdout because sshfs is not capable of connecting with IPv6
				sshfsArgs = append(sshfsArgs,
					"-o", "slave",
					fmt.Sprintf("localhost:%s", mountPoint),
					clientMountPoint, // where to mount it
				)
			} else {
				sshfsArgs = append(sshfsArgs,
					"-o", fmt.Sprintf("directport=%d", podAddrPort.Port()),
					fmt.Sprintf("%s:%s", podIP.String(), mountPoint), // what to mount
					clientMountPoint, // where to mount it
				)
			}

			exe := "sshfs"
			if runtime.GOOS == "windows" {
				// Use sshfs-win to launch the sshfs
				sshfsArgs = append([]string{"cmd", "-ouid=-1", "-ogid=-1"}, sshfsArgs...)
				exe = "sshfs-win"
			}
			var err error
			if useIPv6 {
				var conn net.Conn
				if conn, err = net.Dial("tcp6", podAddrPort.String()); err == nil {
					defer conn.Close()
					err = dpipe.DPipe(ctx, conn, exe, sshfsArgs...)
				}
			} else {
				err = proc.Run(ctx, nil, exe, sshfsArgs...)
			}
			return err
		}, bc)
		if err != nil {
			clog.Error(ctx, err)
		}
	}()
	return nil
}
