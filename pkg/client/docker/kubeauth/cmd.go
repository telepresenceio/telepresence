package kubeauth

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	authGrpc "github.com/telepresenceio/telepresence/v2/pkg/authenticator/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

const (
	CommandName       = "kubeauth-foreground"
	PortFileStaleTime = 3 * time.Second
)

type authService struct {
	portFile  string
	kubeFlags *genericclioptions.ConfigFlags
}

func Command() *cobra.Command {
	as := authService{kubeFlags: genericclioptions.NewConfigFlags(false)}
	c := &cobra.Command{
		Use:    CommandName,
		Short:  "Launch Telepresence Kubernetes authenticator",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE:   as.run,
	}
	flags := c.Flags()
	flags.StringVar(&as.portFile, "portfile", "", "File where server existence is announced.")
	as.kubeFlags.AddFlags(flags)
	return c
}

func (as *authService) run(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		return err
	}
	ctx = client.WithConfig(ctx, cfg)

	if as.portFile == "" {
		return errcat.User.New("missing required flag --portfile")
	}
	grpcListener, err := net.Listen("tcp", ":0")
	if err != nil {
		return errcat.NoDaemonLogs.Newf("unable to open a port on localhost: %w", err)
	}

	ctx, err = logging.InitContext(ctx, "kubeauth", logging.RotateNever, false)
	if err != nil {
		return err
	}
	addr := grpcListener.Addr().(*net.TCPAddr)
	dlog.Infof(ctx, "kubeauth listening on address %s", addr)

	ctx, cancel := context.WithCancel(ctx)
	if err := as.keepPortFileAlive(ctx, cancel, addr.Port); err != nil {
		return err
	}

	grpcHandler := grpc.NewServer()
	authGrpc.RegisterAuthenticatorServer(grpcHandler, as.kubeFlags.ToRawKubeConfigLoader())

	sc := &dhttp.ServerConfig{Handler: grpcHandler}
	return sc.Serve(ctx, grpcListener)
}

func (as *authService) keepPortFileAlive(ctx context.Context, cancel context.CancelFunc, port int) error {
	pb := binary.BigEndian.AppendUint16(nil, uint16(port))
	if err := os.WriteFile(as.portFile, pb, 0o644); err != nil {
		return err
	}
	dlog.Infof(ctx, "kubeauth created %s with %d", as.portFile, port)
	go func() {
		ticker := time.NewTicker(PortFileStaleTime)
		defer func() {
			ticker.Stop()
			_ = os.Remove(as.portFile)
			dlog.Debugf(ctx, "kubeauth removed %s", as.portFile)
		}()
		now := time.Now()
		for {
			if err := os.Chtimes(as.portFile, now, now); err != nil {
				if !os.IsNotExist(err) {
					// File is removed, so stop trying to update its timestamps and die
					dlog.Errorf(ctx, "failed to update timestamp on %s: %v", as.portFile, err)
				}
				dlog.Info(ctx, "kubeauth exiting")
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case now = <-ticker.C:
			}
		}
	}()
	return nil
}
