package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
)

// addPreviewFlags mutates 'flags', adding flags to it such that the flags set the appropriate
// fields in the given 'spec'.  If 'prefix' is given, long-flag names are prefixed with it.
func addPreviewFlags(prefix string, flags *pflag.FlagSet, spec *manager.PreviewSpec) {
	flags.BoolVarP(&spec.DisplayBanner, prefix+"banner", "b", true, "Display banner on preview page")
	flags.StringToStringVarP(&spec.AddRequestHeaders, prefix+"add-request-headers", "", map[string]string{},
		"Additional headers in key1=value1,key2=value2 pairs injected in every preview page request")
}

type UpdateInterceptFn func(context.Context, *manager.UpdateInterceptRequest) (*manager.InterceptInfo, error)

func clientUpdateInterceptFn(client manager.ManagerClient, opts ...grpc.CallOption) UpdateInterceptFn {
	return func(ctx context.Context, req *manager.UpdateInterceptRequest) (*manager.InterceptInfo, error) {
		return client.UpdateIntercept(ctx, req, opts...)
	}
}

func AddPreviewDomain(ctx context.Context, reporter *scout.Reporter, updateInterceptFn UpdateInterceptFn, sess *manager.SessionInfo, iceptName string, previewSpec *manager.PreviewSpec) (*manager.InterceptInfo, error) {
	intercept, err := updateInterceptFn(ctx, &manager.UpdateInterceptRequest{
		Session: sess,
		Name:    iceptName,
		PreviewDomainAction: &manager.UpdateInterceptRequest_AddPreviewDomain{
			AddPreviewDomain: previewSpec,
		},
	})
	if err != nil {
		reporter.Report(ctx, "preview_domain_create_fail", scout.Entry{Key: "error", Value: err.Error()})
		err = fmt.Errorf("creating preview domain: %w", err)
		return nil, err
	}
	reporter.SetMetadatum(ctx, "preview_url", intercept.PreviewDomain)
	reporter.Report(ctx, "preview_domain_create_success")
	return intercept, nil
}

func previewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "preview",
		Args: OnlySubcommands,

		Short: "Create or remove preview domains for existing intercepts",
		RunE:  RunSubcommands,
	}

	var createSpec manager.PreviewSpec
	createCmd := &cobra.Command{
		Use:  "create [flags] <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Create a preview domain for an existing intercept",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConnector(cmd, true, nil, func(ctx context.Context, cs *connectorState) error {
				if _, err := cliutil.ClientEnsureLoggedIn(cmd.Context(), "", cs.userD); err != nil {
					return err
				}
				reporter := scout.NewReporter(ctx, "cli")
				reporter.Start(ctx)
				return cliutil.WithManager(ctx, func(ctx context.Context, managerClient manager.ManagerClient) error {
					if createSpec.Ingress == nil {
						request := manager.GetInterceptRequest{Session: cs.SessionInfo, Name: args[0]}
						// Throws rpc "not found" error if intercept has not yet been created
						interceptInfo, err := managerClient.GetIntercept(ctx, &request)
						if err != nil {
							return err
						}
						iis, err := cs.userD.GetIngressInfos(ctx, &empty.Empty{})
						if err != nil {
							return err
						}
						ingress, err := selectIngress(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), cs.ConnectInfo, interceptInfo.Spec.Agent, interceptInfo.Spec.Namespace, iis.IngressInfos)
						if err != nil {
							return err
						}
						createSpec.Ingress = ingress
					}
					intercept, err := AddPreviewDomain(ctx, reporter,
						clientUpdateInterceptFn(managerClient),
						cs.SessionInfo,
						args[0], // intercept name
						&createSpec)
					if err != nil {
						return err
					}
					fmt.Println(DescribeIntercept(intercept, nil, false))
					return nil
				})
			})
		},
	}
	addPreviewFlags("", createCmd.Flags(), &createSpec)

	removeCmd := &cobra.Command{
		Use:  "remove <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Remove a preview domain from an intercept",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConnector(cmd, true, nil, func(ctx context.Context, cs *connectorState) error {
				return cliutil.WithManager(ctx, func(ctx context.Context, managerClient manager.ManagerClient) error {
					intercept, err := managerClient.UpdateIntercept(ctx, &manager.UpdateInterceptRequest{
						Session: cs.SessionInfo,
						Name:    args[0],
						PreviewDomainAction: &manager.UpdateInterceptRequest_RemovePreviewDomain{
							RemovePreviewDomain: true,
						},
					})
					if err != nil {
						return err
					}
					fmt.Println(DescribeIntercept(intercept, nil, false))
					return nil
				})
			})
		},
	}

	cmd.AddCommand(createCmd, removeCmd)

	return cmd
}
