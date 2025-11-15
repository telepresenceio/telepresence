package cmd

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type serveCommand struct {
	port uint16
}

func serveCmd() *cobra.Command {
	sc := &serveCommand{}
	cmd := &cobra.Command{
		Use:   "serve <name of remote service>",
		Args:  cobra.ExactArgs(1),
		Short: "Start the browser on a remote service",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          sc.run,
	}
	sc.addFlags(cmd)
	return cmd
}

func (sc *serveCommand) addFlags(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.Uint16VarP(&sc.port, "port", "p", 80, "service port")
}

func (sc *serveCommand) run(cmd *cobra.Command, args []string) error {
	svc := args[0]
	if len(svc) == 0 {
		return errcat.User.New("an empty string is never a valid service name")
	}
	err := connect.InitCommand(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	// Cancel everything on exit
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		cancel()
	}()

	uc := daemon.MustGetUserClient(ctx)
	ip, err := uc.Lookup(ctx, svc)
	if err != nil {
		return err
	}
	if uc.Containerized() {
		err = sc.serveFromContainer(ctx, ip)
	} else {
		err = sc.serveFromHost(ctx, ip)
	}
	return err
}

const (
	browserProgressID = "Web-browser"
	socatProgressID   = "Port-forward"
)

func (sc *serveCommand) serveFromContainer(ctx context.Context, addr netip.Addr) error {
	// We can't reliably just map a service port (typically port 80) to localhost, so instead of doing
	// that, we create a random port and use that.
	ps, err := ioutil.FreePortsTCP(1)
	if err != nil {
		return err
	}
	rndPort := ps[0].Port()
	ap := netip.AddrPortFrom(addr, sc.port)

	progress.Start(ctx, "Serving web browser from container")
	defer progress.Stop(ctx)

	ctx = progress.WithEventId(ctx, socatProgressID)
	progress.Workingf(ctx, "Starting port-forward %d:%s", rndPort, ap)
	cni, cc, err := docker.Start(ctx, true, "-p", fmt.Sprintf("%d:%d", rndPort, rndPort), "--rm",
		"alpine/socat", fmt.Sprintf("TCP-LISTEN:%d,fork", rndPort), fmt.Sprintf("TCP:%s", ap))
	if err != nil {
		return errcat.User.New(err)
	}
	progress.Workingf(ctx, "Started port-forward %d:%s", rndPort, ap)
	go func() {
		<-ctx.Done()
		_ = docker.StopContainer(context.WithoutCancel(ctx), cni.ID)
	}()

	proto := "http"
	on, err := url.Parse(fmt.Sprintf("%s://%s", proto, net.JoinHostPort("localhost", fmt.Sprintf("%d", rndPort))))
	if err != nil {
		return errcat.User.New(err)
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	sc.openBrowser(progress.WithEventId(ctx, browserProgressID), on, wg)
	err = cc.Wait()
	if err != nil && ctx.Err() == nil {
		return errcat.NoDaemonLogs.New(cc.Wait())
	}
	wg.Wait()
	progress.Donef(ctx, "Stopped port-forward %d:%s", rndPort, ap)
	return nil
}

func (sc *serveCommand) serveFromHost(ctx context.Context, addr netip.Addr) error {
	proto := "http"
	ap := netip.AddrPortFrom(addr, sc.port)
	on, err := url.Parse(fmt.Sprintf("%s://%s", proto, ap))
	if err != nil {
		return errcat.User.New(err)
	}

	progress.Start(ctx, "Serving web browser from host")
	defer progress.Stop(ctx)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	sc.openBrowser(progress.WithEventId(ctx, browserProgressID), on, wg)
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (sc *serveCommand) openBrowser(ctx context.Context, on *url.URL, wg *sync.WaitGroup) {
	onStr := on.String()
	working := progress.Workingf(ctx, "Opening on %s", onStr)

	// The browser might not open an existing session, in which case the OpenURL call will wait for the browser to close.
	// We don't want to wait here regardless, so we use a separate go-routine to start it and produce initial output on stdout/stderr.
	// This context is canceled either due to a quickly returning OpenURL (existing session) or after a second when
	// the context times out.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	go func() {
		defer func() {
			wg.Done()
			cancel()
			browser.Stderr = os.Stderr
			browser.Stdout = os.Stdout
		}()
		browser.Stderr = working.Pump(ctx, progress.EventStatusWarning)
		browser.Stdout = working.Pump(ctx, progress.EventStatusInfo)
		err := browser.OpenURL(onStr)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}()
	<-ctx.Done()
	progress.Donef(ctx, "Browser opened on %s", onStr)
}
