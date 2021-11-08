package cli

import (
	"fmt"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"google.golang.org/protobuf/types/known/emptypb"
)

type Version struct {
	*BaseDiag
}

func (d *Version) diagName() string {
	return "Version"
}

func (d *Version) run() error {
	version := client.Version()
	ctx := d.cmd.Context()
	userDaemonVersion, err := daemonVersion(ctx)
	if err != nil {
		return err
	}
	rootDaemonVersion, err := d.cc.Version(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	managerVersion, err := d.mc.Version(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	if userDaemonVersion.Version != version && rootDaemonVersion.Version != version && managerVersion.Version != version {
		return fmt.Errorf("version mismatch: client version: %s, user daemon version: %s, root daemon version: %s, manager version: %s", version, userDaemonVersion.Version, rootDaemonVersion.Version, managerVersion.Version)
	}
	return nil
}
