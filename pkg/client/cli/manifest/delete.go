package manifest

import (
	"fmt"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	tpgrpc "github.com/telepresenceio/telepresence/v2/pkg/grpc"
)

// Delete tears down the workstation state described by st; see the "Command semantics" section
// of docs/plans/state-manifest/plan.md for the exact rules.
func Delete(cmd *cobra.Command, st *State) error {
	connect.InitProgressWriter(cmd)
	cmd.SetContext(dos.WithStdio(cmd.Context(), cmd))
	defer progress.Stop(cmd.Context())

	var connLine string
	if st.Connection != nil {
		cr, err := buildConnectRequest(st.Connection)
		if err != nil {
			return err
		}
		id, err := daemon.IdentifierFromFlags(cmd.Context(), cr.Name, cr.KubeFlags, cr.KubeconfigData, false)
		if err != nil {
			return err
		}
		info, err := findConnectionInfo(cmd.Context(), id)
		if err != nil {
			return err
		}
		if info == nil {
			printSummary(cmd, "not running; nothing to tear down", nil)
			return nil
		}
		newCtx, err := connect.ExistingDaemon(cmd.Context(), info)
		if err != nil {
			return err
		}
		ci, err := daemon.MustGetUserClient(newCtx).Status(newCtx, &empty.Empty{})
		if err != nil {
			return tpgrpc.FromGRPC(err)
		}
		if ci.ManagerVersion == nil {
			// The daemon is running but has no session, so neither attachments nor a
			// disconnect can apply.
			printSummary(cmd, "not connected; nothing to tear down", nil)
			return nil
		}
		cmd.SetContext(newCtx)
	} else if err := requireCurrentSession(cmd); err != nil {
		return err
	}

	results := make([]attachmentResult, 0, len(st.Attachments))
	for i := len(st.Attachments) - 1; i >= 0; i-- {
		a := &st.Attachments[i]
		res, err := removeAttachment(cmd, a)
		if err != nil {
			return fmt.Errorf("attachment %q: %w", a.Name, err)
		}
		results = append(results, res)
	}

	if st.Connection != nil {
		connect.Disconnect(cmd.Context())
		connLine = "disconnected"
	}
	printSummary(cmd, connLine, results)
	return nil
}
