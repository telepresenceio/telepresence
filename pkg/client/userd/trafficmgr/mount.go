package trafficmgr

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dpipe"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type mountForward struct {
	forwardKey
	SftpPort         int32
	RemoteMountPoint string
}

// shouldMount returns true if the intercept info given should result in mounts
func (tm *TrafficManager) shouldMount(ii *manager.InterceptInfo) bool {
	return ii.SftpPort > 0
}

// startMount starts the mount for the given forwardKey.
// It assumes that the user has called shouldMount and is sure that something will be started.
func (tm *TrafficManager) startMount(ctx context.Context, wg *sync.WaitGroup, fk forwardKey, sftpPort int32, remoteMountPoint string) {
	if sftpPort > 0 {
		// There's nothing to mount if the SftpPort is zero
		mntCtx := dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s:%d", fk.PodIP, sftpPort))
		wg.Add(1)
		go tm.workerMountForwardIntercept(mntCtx, mountForward{fk, sftpPort, remoteMountPoint}, wg)
	}
}

func (tm *TrafficManager) mountPointForIntercept(name string) (mountPoint string) {
	tm.mountPoints.Range(func(key, value any) bool {
		if name == value.(string) {
			mountPoint = key.(string)
			return false
		}
		return true
	})
	return
}

func (tm *TrafficManager) workerMountForwardIntercept(ctx context.Context, mf mountForward, wg *sync.WaitGroup) {
	defer wg.Done()

	mountPoint := tm.mountPointForIntercept(mf.Name)
	if mountPoint == "" {
		// User has explicitly specified that no mount should take place
		dlog.Infof(ctx, "No mount point found for intercept %q", mf.Name)
		return
	}

	dlog.Infof(ctx, "Mounting file system for intercept %q at %q", mf.Name, mountPoint)

	// The mounts performed here are synced on by podIP + sftpPort to keep track of active
	// mounts. This is not enough in situations when a pod is deleted and another pod
	// takes over. That is two different IPs so an additional synchronization on the actual
	// mount point is necessary to prevent that it is established and deleted at the same
	// time.
	mountMutex := new(sync.Mutex)
	mountMutex.Lock()
	if oldMutex, loaded := tm.mountMutexes.LoadOrStore(mountPoint, mountMutex); loaded {
		mountMutex.Unlock() // not stored, so unlock and throw away
		mountMutex = oldMutex.(*sync.Mutex)
		mountMutex.Lock()
	}

	defer func() {
		tm.mountMutexes.Delete(mountPoint)
		mountMutex.Unlock()
	}()

	// Retry mount in case it gets disconnected
	err := client.Retry(ctx, "sshfs", func(ctx context.Context) error {
		dl := &net.Dialer{Timeout: 3 * time.Second}
		conn, err := dl.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", mf.PodIP, mf.SftpPort))
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
			"localhost:" + mf.RemoteMountPoint, // what to mount
			mountPoint,                         // where to mount it
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
		_ = proc.CommandContext(ctx, "fusermount", "-uz", mountPoint).Run()
		return err
	}, 3*time.Second, 6*time.Second)

	if err != nil && ctx.Err() == nil {
		dlog.Error(ctx, err)
	}
}
