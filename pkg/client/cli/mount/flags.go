package mount

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type Flags struct {
	LocalMountPort uint16 // --local-mount-port
	Mount          string // --mount // "true", "false", or desired mount point
	Enabled        bool
	ReadOnly       bool
}

func (f *Flags) AddFlags(flagSet *pflag.FlagSet, forceReadOnly bool) {
	mountText := `The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use "true" to ` +
		`have Telepresence pick a random mount point (default). Use "false" to disable filesystem mounting entirely.`
	if !forceReadOnly {
		mountText += ` Append ":ro" to mount everything read-only.`
	}
	flagSet.StringVar(&f.Mount, "mount", "true", mountText)

	flagSet.Uint16Var(&f.LocalMountPort, "local-mount-port", 0,
		`Do not mount remote directories. Instead, expose this port on localhost to an external mounter`)
	f.ReadOnly = forceReadOnly
}

func (f *Flags) Validate(cmd *cobra.Command) error {
	if f.LocalMountPort > 0 && client.GetConfig(cmd.Context()).Intercept().UseFtp {
		return errcat.User.New("only SFTP can be used with --local-mount-port. Client is configured to perform remote mounts using FTP")
	}
	if !cmd.Flag("mount").Changed {
		// Default is that mount is enabled and the path is unspecified
		f.Mount = "" // Get rid of the default string "true"
		f.Enabled = true
	} else if len(f.Mount) > 0 {
		if strings.HasSuffix(f.Mount, ":ro") {
			f.ReadOnly = true
			f.Mount = f.Mount[:len(f.Mount)-3]
		}
		doMount, err := strconv.ParseBool(f.Mount)
		if err != nil {
			// Not a boolean flag. Must be a path then
			f.Enabled = true
		} else {
			// Boolean flag, path unspecified
			f.Enabled = doMount
			f.Mount = ""
			f.LocalMountPort = 0
		}
	}
	return nil
}

func (f *Flags) ValidateConnected(ctx context.Context) (err error) {
	if !f.Enabled {
		return nil
	}
	defer func() {
		if err != nil {
			f.Enabled = false
			f.Mount = ""
			f.LocalMountPort = 0
		}
	}()

	ud := daemon.MustGetUserClient(ctx)
	if ud.Containerized() {
		// Mounts will be facilitated by the Telemount plug-in connecting to our LocalMountPort
		if f.LocalMountPort == 0 {
			var lma []netip.AddrPort
			lma, err = ioutil.FreePortsTCP(1, client.GetConfig(ctx).Docker().EnableIPv6)
			if err != nil {
				return err
			}
			f.LocalMountPort = lma[0].Port()
		}
		return nil
	}

	if err = checkCapability(ctx); err != nil {
		err = fmt.Errorf("remote volume mounts are disabled: %w", err)
		// Log a warning and disable, but continue
		f.Enabled = false
		f.Mount = ""
		f.LocalMountPort = 0
		dlog.Warning(ctx, err)
		return err
	}

	var cwd string
	cwd, err = os.Getwd()
	if err != nil {
		return err
	}
	f.Mount, err = prepare(ctx, cwd, f.Mount)
	return err
}

func checkCapability(ctx context.Context) error {
	_, err := daemon.MustGetUserClient(ctx).RemoteMountAvailability(ctx, &empty.Empty{})
	if err != nil {
		err = grpc.FromGRPC(err)
	}
	return err
}
