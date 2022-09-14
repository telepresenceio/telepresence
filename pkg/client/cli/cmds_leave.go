package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

func leaveCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "leave [flags] <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Remove existing intercept",
		RunE: func(cmd *cobra.Command, args []string) error {
			return removeIntercept(cmd.Context(), strings.TrimSpace(args[0]))
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			var (
				completions  = []string{}
				intercepts   = []*connector.WorkloadInfo{}
				shellCompDir = cobra.ShellCompDirectiveNoFileComp
			)

			if len(args) != 0 {
				return completions, shellCompDir
			}

			err := cliutil.WithStartedConnector(cmd.Context(), true, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
				resp, err := connectorClient.List(ctx, &connector.ListRequest{
					Filter:    connector.ListRequest_INTERCEPTS,
					Namespace: "",
				})
				if err != nil {
					return err
				}

				intercepts = resp.Workloads

				return nil
			})
			if err != nil {
				dlog.Debugf(cmd.Context(), "error getting intercepts: %v", err)
				shellCompDir |= cobra.ShellCompDirectiveError
			}

			if len(intercepts) == 0 {
				return completions, shellCompDir
			}

			for _, intercept := range intercepts {
				for _, ii := range intercept.InterceptInfos {
					name := ii.Spec.Name
					if strings.HasPrefix(name, toComplete) {
						completions = append(completions, name)
					}
				}
			}

			return completions, shellCompDir
		},
	}
}

// InterceptError inspects the .Error and .ErrorText fields in an InterceptResult and returns an
// appropriate error object, or nil if the InterceptResult doesn't represent an error.
func InterceptError(r *connector.InterceptResult) error {
	msg := ""
	errCat := errcat.Unknown
	switch r.Error {
	case common.InterceptError_UNSPECIFIED:
		return nil
	case common.InterceptError_NO_CONNECTION:
		msg = "Local network is not connected to the cluster"
	case common.InterceptError_NO_TRAFFIC_MANAGER:
		msg = "Intercept unavailable: no traffic manager"
	case common.InterceptError_TRAFFIC_MANAGER_CONNECTING:
		msg = "Connecting to traffic manager..."
	case common.InterceptError_TRAFFIC_MANAGER_ERROR:
		msg = r.ErrorText
	case common.InterceptError_ALREADY_EXISTS:
		msg = fmt.Sprintf("Intercept with name %q already exists", r.ErrorText)
	case common.InterceptError_NAMESPACE_AMBIGUITY:
		nss := strings.Split(r.ErrorText, ",")
		msg = fmt.Sprintf(
			"A workstation cannot have simultaneous intercepts in different namespaces. Leave all intercepts in %q before creting new ones in %q",
			nss[0], nss[1])
	case common.InterceptError_LOCAL_TARGET_IN_USE:
		spec := r.InterceptInfo.Spec
		msg = fmt.Sprintf("Port %s:%d is already in use by intercept %s",
			spec.TargetHost, spec.TargetPort, spec.Name)
	case common.InterceptError_NO_ACCEPTABLE_WORKLOAD:
		msg = fmt.Sprintf("No interceptable deployment, replicaset, or statefulset matching %s found", r.ErrorText)
	case common.InterceptError_AMBIGUOUS_MATCH:
		var matches []manager.AgentInfo
		err := json.Unmarshal([]byte(r.ErrorText), &matches)
		if err != nil {
			msg = fmt.Sprintf("Unable to unmarshal JSON: %v", err)
			break
		}
		st := &strings.Builder{}
		fmt.Fprintf(st, "Found more than one possible match:")
		for idx := range matches {
			match := &matches[idx]
			fmt.Fprintf(st, "\n%4d: %s.%s", idx+1, match.Name, match.Namespace)
		}
		msg = st.String()
	case common.InterceptError_FAILED_TO_ESTABLISH:
		msg = fmt.Sprintf("Failed to establish intercept: %s", r.ErrorText)
	case common.InterceptError_UNSUPPORTED_WORKLOAD:
		msg = fmt.Sprintf("Unsupported workload type: %s", r.ErrorText)
	case common.InterceptError_NOT_FOUND:
		msg = fmt.Sprintf("Intercept named %q not found", r.ErrorText)
	case common.InterceptError_MOUNT_POINT_BUSY:
		msg = fmt.Sprintf("Mount point already in use by intercept %q", r.ErrorText)
	case common.InterceptError_MISCONFIGURED_WORKLOAD:
		msg = r.ErrorText
	case common.InterceptError_UNKNOWN_FLAG:
		msg = fmt.Sprintf("Unknown flag: %s", r.ErrorText)
	default:
		msg = fmt.Sprintf("Unknown error code %d", r.Error)
	}
	if r.ErrorCategory > 0 {
		errCat = errcat.Category(r.ErrorCategory)
	}

	if id := r.GetInterceptInfo().GetId(); id != "" {
		msg = fmt.Sprintf("%s: id = %q", msg, id)
	}
	return errCat.Newf(msg)
}

func removeIntercept(ctx context.Context, name string) error {
	return cliutil.WithStartedConnector(ctx, true, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var r *connector.InterceptResult
		var err error
		r, err = connectorClient.RemoveIntercept(dcontext.WithoutCancel(ctx), &manager.RemoveInterceptRequest2{Name: name})
		if err != nil {
			return err
		}
		if r.Error != common.InterceptError_UNSPECIFIED {
			return InterceptError(r)
		}
		return nil
	})
}
