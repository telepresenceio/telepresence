package userd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const latestFuseFTPRelease = "https://github.com/datawire/go-fuseftp/releases/latest/download/fuseftp-%s-%s%s"

// downloadFuseFTPBinary checks if the binary is present under the given dir, and if not
// downloads the latest released version into a binary executable file.
func downloadFuseFTPBinary(ctx context.Context, exe, dir string) error {
	// Perform the actual download
	qn := filepath.Join(dir, exe)
	dest, err := os.OpenFile(qn, os.O_WRONLY|os.O_CREATE, 0700)
	if err != nil {
		return err
	}
	defer func() {
		dest.Close()
		if err != nil {
			_ = os.Remove(qn)
		}
	}()

	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}

	downloadURL := fmt.Sprintf(latestFuseFTPRelease, runtime.GOOS, runtime.GOARCH, suffix)
	dlog.Debugf(ctx, "About to download fuseftp from %s", downloadURL)
	var resp *http.Response
	resp, err = http.Get(downloadURL)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = errors.New(resp.Status)
		}
	}
	if err != nil {
		err = fmt.Errorf("failed to download %s: %v", downloadURL, err)
		return err
	}

	dlog.Debugf(ctx, "Downloading %s...", downloadURL)
	_, err = io.Copy(dest, resp.Body)
	return err
}

// runFuseFtpServer ensures that the fuseftp gRPC server is downloaded into the
// user cache, and starts it. Once the socket is created by the server, a
// client is connected and written to the given channel.
//
// The server dies when the given context is cancelled.
func runFuseFTPServer(ctx context.Context, cCh chan<- rpc.FuseFTPClient) error {
	dir, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		close(cCh)
		return err
	}
	exe := "fuseftp"
	if runtime.GOOS == "windows" {
		exe = "fuseftp.exe"
	}
	qn := filepath.Join(dir, exe)
	_, err = os.Stat(qn)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if err = downloadFuseFTPBinary(ctx, exe, dir); err != nil {
				close(cCh)
				return err
			}
		}
	}

	sf, err := os.CreateTemp("", "fuseftp-*.socket")
	if err != nil {
		close(cCh)
		return err
	}
	socketName := sf.Name()
	_ = sf.Close()
	_ = os.Remove(socketName)

	go func() {
		defer close(cCh)
		if err = client.WaitUntilSocketAppears("fuseftp", socketName, 10*time.Second); err != nil {
			return
		}
		conn, err := client.DialSocket(ctx, socketName)
		if err != nil {
			dlog.Errorf(ctx, "unable to dial fuseftp socket")
			return
		}
		select {
		case <-ctx.Done():
		case cCh <- rpc.NewFuseFTPClient(conn):
		}
	}()

	cmd := proc.CommandContext(ctx, qn, socketName)

	// Rely on that these have been redirected to use our logger
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.DisableLogging = true
	return cmd.Run()
}
