package kubeauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	authGrpc "github.com/telepresenceio/telepresence/v2/pkg/authenticator/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

const (
	CommandName       = "kubeauth-foreground"
	PortFileDir       = "kubeauth"
	PortFileStaleTime = 3 * time.Second
)

type authService struct {
	portFile    string
	kubeFlags   *genericclioptions.ConfigFlags
	cancel      context.CancelFunc
	configFiles []string
}

type PortFile struct {
	Port       int    `json:"port"`
	Kubeconfig string `json:"kubeconfig"`
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

	config := as.kubeFlags.ToRawKubeConfigLoader()
	as.configFiles = config.ConfigAccess().GetLoadingPrecedence()
	p := PortFile{
		Port:       addr.Port,
		Kubeconfig: strings.Join(as.configFiles, string(filepath.ListSeparator)),
	}
	pb, err := json.Marshal(&p)
	if err != nil {
		return err
	}
	if err = os.WriteFile(as.portFile, pb, 0o644); err != nil {
		return err
	}

	ctx, as.cancel = context.WithCancel(ctx)
	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
	g.Go("portfile-alive", as.keepPortFileAlive)
	g.Go("portfile-watcher", as.watchFiles)
	g.Go("grpc-server", func(ctx context.Context) error {
		grpcHandler := grpc.NewServer()
		authGrpc.RegisterAuthenticatorServer(grpcHandler, config)
		sc := &dhttp.ServerConfig{Handler: grpcHandler}
		return sc.Serve(ctx, grpcListener)
	})
	return g.Wait()
}

func (as *authService) keepPortFileAlive(ctx context.Context) error {
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
				return fmt.Errorf("failed to update timestamp on %s: %v", as.portFile, err)
			}
			// File is removed, so stop trying to update its timestamps and die
			dlog.Info(ctx, "kubeauth exiting")
			as.cancel()
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case now = <-ticker.C:
		}
	}
}

func (as *authService) watchFiles(ctx context.Context) error {
	// If any of the files that the current kubeconfig uses change, then we die
	files := as.configFiles

	// If the portFile changes, then we die
	files = append(files, as.portFile)

	dirs := make(map[string]struct{})
	for _, file := range files {
		dir := filepath.Dir(file)
		dirs[dir] = struct{}{}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	isOfInterest := func(s string, files []string) bool {
		for _, file := range files {
			if s == file {
				return true
			}
		}
		return false
	}
	for dir := range dirs {
		// Can't watch things that don't exist. We want to know if files in there change though.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err = watcher.Add(dir); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err = <-watcher.Errors:
			dlog.Error(ctx, err)
		case event := <-watcher.Events:
			if event.Op&(fsnotify.Remove|fsnotify.Write|fsnotify.Create) != 0 && isOfInterest(event.Name, files) {
				dlog.Infof(ctx, "Terminated due to %s in %s", event.Op, event.Name)
				as.cancel()
			}
		}
	}
}
