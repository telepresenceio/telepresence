package flags

import (
	"context"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
)

// NodeAgentDefault resolves the effective value of a --node-agent flag.
//
// Precedence, highest first, is an invariant of this function:
//
//  1. An explicitly passed --node-agent flag (flagValue) always wins, be it
//     true or false.
//  2. The connected session's merged client config nodeAgent.enabled. This
//     is the traffic-manager's cluster-wide client.nodeAgent.enabled Helm
//     value (charts/telepresence-oss/templates/trafficManager-configmap.yaml),
//     merged with (and overridable by) the workstation's local config.yml
//     the same way every other client config setting is: the local value
//     wins only when it differs from the zero value, so a local
//     nodeAgent.enabled: true overrides a cluster-side false, but a local
//     config that leaves nodeAgent.enabled unset (or false) cannot force
//     off a cluster-side true — see (*config).DestructiveMerge /
//     mergeNonDefaults in pkg/client/config.go.
//  3. false.
//
// cmd's context is expected to already carry a connected user daemon
// client by the time this is called (i.e. call it after
// connect.InitCommand has run for a command with a required or optional
// session) so that step 2 can reach the session-merged config. When no
// user daemon client is available in the context, or the RPC/unmarshal
// fails (e.g. an older daemon that predates this call), this degrades
// gracefully: it logs the problem at debug level and falls back to the
// LOCAL client.GetConfig(ctx) value, preserving the pre-existing,
// local-only behavior.
func NodeAgentDefault(cmd *cobra.Command, flagValue bool) bool {
	if cmd.Flags().Changed("node-agent") {
		return flagValue
	}
	return sessionNodeAgentEnabled(cmd.Context())
}

// sessionNodeAgentEnabled returns the nodeAgent.enabled setting from the
// connected session's merged client config, falling back to the local
// client.GetConfig(ctx) value when no session is available or the session
// config can't be obtained/parsed.
func sessionNodeAgentEnabled(ctx context.Context) bool {
	if uc := daemon.GetUserClient(ctx); uc != nil {
		if cc, err := uc.GetConfig(ctx, &empty.Empty{}); err != nil {
			clog.Debugf(ctx, "node-agent default: unable to get session config, falling back to local config: %v", err)
		} else {
			var sc client.SessionConfig
			if err = json.Unmarshal(cc.Json, &sc, false); err != nil {
				clog.Debugf(ctx, "node-agent default: unable to parse session config, falling back to local config: %v", err)
			} else {
				return sc.NodeAgent().Enabled
			}
		}
	}
	return client.GetConfig(ctx).NodeAgent().Enabled
}
