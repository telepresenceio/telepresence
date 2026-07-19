package manifest

import (
	"fmt"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	tpgrpc "github.com/telepresenceio/telepresence/v2/pkg/grpc"
)

// Apply brings the workstation state described by st to the desired state; see the
// "Apply semantics" section of docs/reference/state-manifest.md for the exact rules.
func Apply(cmd *cobra.Command, st *State, dryRun bool) error {
	connect.InitProgressWriter(cmd)
	cmd.SetContext(dos.WithStdio(cmd.Context(), cmd))
	defer progress.Stop(cmd.Context())

	var connLine string
	if st.Connection != nil {
		var wouldConnect bool
		var err error
		connLine, wouldConnect, err = ensureDeclaredConnection(cmd, st.Connection, dryRun)
		if err != nil {
			return err
		}
		if wouldConnect {
			results := make([]attachmentResult, len(st.Attachments))
			for i, a := range st.Attachments {
				results[i] = attachmentResult{Name: a.Name, Type: string(a.Type), Action: "would-create"}
				if a.Command != nil {
					results[i].Handler = "would-start"
				}
			}
			printSummary(cmd, connLine, results)
			return nil
		}
	} else if err := requireCurrentSession(cmd); err != nil {
		return err
	}

	results := make([]attachmentResult, 0, len(st.Attachments))
	for i := range st.Attachments {
		a := &st.Attachments[i]
		res, err := reconcileAttachment(cmd, a, dryRun)
		if err != nil {
			return fmt.Errorf("attachment %q: %w", a.Name, err)
		}
		results = append(results, res)
	}
	printSummary(cmd, connLine, results)
	return nil
}

// ensureDeclaredConnection resolves a manifest-declared connection.
//
// When a matching daemon is already running, the manifest's request is first checked against its
// session with the read-only CheckConnect RPC. Drift is a User error, and because Connect is
// never attempted on a drifted session, the connect flow's failure handling (which deletes the
// daemon info file and thereby shuts the daemon down along with every attachment it carries)
// can't be triggered by a drifted manifest.
//
// Only an aligned or session-less state proceeds: a real apply then goes through
// connect.InitCommand with the manifest's request in context, exactly like a repeated
// `telepresence connect`, whereas a dry run reports "would-connect" (or keeps the aligned
// daemon's context for the per-attachment comparison) and touches nothing.
func ensureDeclaredConnection(cmd *cobra.Command, conn *Connection, dryRun bool) (connLine string, wouldConnect bool, err error) {
	ctx := cmd.Context()
	cr, err := buildConnectRequest(conn)
	if err != nil {
		return "", false, err
	}
	id, err := daemon.IdentifierFromFlags(ctx, cr.Name, cr.KubeFlags, cr.KubeconfigData, false)
	if err != nil {
		return "", false, err
	}
	info, err := findConnectionInfo(ctx, id)
	if err != nil {
		return "", false, err
	}
	if info == nil {
		if dryRun {
			return "would-connect", true, nil
		}
		return connectDeclared(cmd, cr)
	}

	newCtx, err := connect.ExistingDaemon(ctx, info)
	if err != nil {
		return "", false, err
	}
	ud := daemon.MustGetUserClient(newCtx)
	if _, err := ud.CheckConnect(newCtx, cr.ConnectRequest); err != nil {
		_ = ud.Close()
		switch status.Code(err) {
		case codes.Unavailable, codes.Canceled:
			// The daemon is running but has no session (or the session is shutting down), so
			// there is nothing to drift from and no per-attachment comparison is possible.
			if dryRun {
				return "would-connect", true, nil
			}
			return connectDeclared(cmd, cr)
		}
		if err = tpgrpc.FromGRPC(err); errcat.GetCategory(err) != errcat.User {
			return "", false, err
		}
		return "", false, errcat.User.Newf("connection %s has drifted from the manifest: %v", id.Name, err)
	}
	if dryRun {
		cmd.SetContext(newCtx)
		return "reused", false, nil
	}
	_ = ud.Close()
	return connectDeclared(cmd, cr)
}

// connectDeclared runs the ordinary connect flow with the manifest's request in context. The
// session must already be known to be aligned (or absent), so a failure here is a genuine
// connect failure and not manifest drift.
func connectDeclared(cmd *cobra.Command, cr *daemon.Request) (connLine string, wouldConnect bool, err error) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[ann.Session] = ann.Required
	cmd.SetContext(daemon.WithRequest(cmd.Context(), cr))
	if err := connect.InitCommand(cmd); err != nil {
		return "", false, err
	}
	connLine = "reused"
	if s := daemon.GetSession(cmd.Context()); s != nil && s.Started {
		connLine = "connected"
	}
	return connLine, false, nil
}
