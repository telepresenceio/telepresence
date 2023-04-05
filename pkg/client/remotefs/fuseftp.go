//go:build !docker
// +build !docker

package remotefs

import (
	"context"
	_ "embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

//go:embed fuseftp.bits
var fuseftpBits []byte

type fuseFtpMgr struct {
	startFuseCh chan struct{}
	fuseFtpCh   chan rpc.FuseFTPClient
}

type FuseFTPManager interface {
	DeferInit(ctx context.Context) error
	GetFuseFTPClient(ctx context.Context) rpc.FuseFTPClient
}

func NewFuseFTPManager() FuseFTPManager {
	return &fuseFtpMgr{
		startFuseCh: make(chan struct{}),
		fuseFtpCh:   make(chan rpc.FuseFTPClient, 1),
	}
}

func (s *fuseFtpMgr) DeferInit(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-s.startFuseCh:
	}
	return runFuseFTPServer(ctx, s.fuseFtpCh)
}

func (s *fuseFtpMgr) GetFuseFTPClient(ctx context.Context) rpc.FuseFTPClient {
	// Close startFuseFtp unless it's already closed. This will kick
	// the DeferInit to either make the client available on
	// the fuseFtpCh or close that channel
	select {
	case <-s.startFuseCh:
	default:
		close(s.startFuseCh)
	}

	select {
	case <-ctx.Done():
		return nil
	case c, ok := <-s.fuseFtpCh:
		if ok {
			// Put the client back onto the queue for the next caller to read
			s.fuseFtpCh <- c
		}
		return c
	}
}

// runFuseFtpServer ensures that the fuseftp gRPC server is downloaded into the
// user cache, and starts it. Once the socket is created by the server, a
// client is connected and written to the given channel.
//
// The server dies when the given context is cancelled.
func runFuseFTPServer(ctx context.Context, cCh chan<- rpc.FuseFTPClient) error {
	closeCh := true
	defer func() {
		if closeCh {
			close(cCh)
		}
	}()

	exe := "fuseftp"
	if runtime.GOOS == "windows" {
		exe = "fuseftp.exe"
	}
	qn := filepath.Join(filelocation.AppUserCacheDir(ctx), exe)
	var sz int
	st, err := os.Stat(qn)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		sz = 0
	} else {
		sz = int(st.Size())
	}

	if len(fuseftpBits) != sz {
		if err = os.WriteFile(qn, fuseftpBits, 0o700); err != nil {
			return err
		}
	}

	sf, err := os.CreateTemp("", "fuseftp-*.socket")
	if err != nil {
		return err
	}
	socketName := sf.Name()
	_ = sf.Close()
	_ = os.Remove(socketName)

	closeCh = false // closing the channel is now the responsibility of waitForSocketAndConnect
	go waitForSocketAndConnect(ctx, socketName, cCh)

	cmd := proc.CommandContext(ctx, qn, socketName)

	// Rely on that these have been redirected to use our logger
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.DisableLogging = true
	return cmd.Run()
}

func waitForSocketAndConnect(ctx context.Context, socketName string, cCh chan<- rpc.FuseFTPClient) {
	giveUp := time.After(3 * time.Second)
	for {
		select {
		case <-ctx.Done():
			close(cCh)
			return
		case <-giveUp:
			close(cCh)
			dlog.Error(ctx, "timeout waiting for fuseftp socket")
			return
		default:
			conn, err := grpc.DialContext(ctx, "unix:"+socketName,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithNoProxy(),
				grpc.WithBlock(),
				grpc.FailOnNonTempDialError(true),
			)
			if err != nil {
				dtime.SleepWithContext(ctx, time.Millisecond)
				continue
			}
			select {
			case <-ctx.Done():
			case cCh <- rpc.NewFuseFTPClient(conn):
			}
			return
		}
	}
}
