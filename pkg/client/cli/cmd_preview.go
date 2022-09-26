package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
)

// AddPreviewFlags mutates 'flags', adding flags to it such that the flags set the appropriate
// fields in the given 'spec'.  If 'prefix' is given, long-flag names are prefixed with it.
func AddPreviewFlags(prefix string, flags *pflag.FlagSet, spec *manager.PreviewSpec) {
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

func AddPreviewDomain(ctx context.Context, reporter *scout.Reporter, updateInterceptFn UpdateInterceptFn, sess *manager.SessionInfo, iceptName string,
	previewSpec *manager.PreviewSpec) (*manager.InterceptInfo, error) {
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
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
	}

	var createSpec manager.PreviewSpec
	createCmd := &cobra.Command{
		Use:  "create [flags] <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Create a preview domain for an existing intercept",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cliutil.InitCommand(cmd); err != nil {
				return err
			}
			ctx := cmd.Context()
			if _, err := cliutil.EnsureLoggedIn(ctx, ""); err != nil {
				return err
			}
			reporter := scout.NewReporter(ctx, "cli")
			reporter.Start(ctx)
			session := cliutil.GetSession(ctx)
			return cliutil.WithManager(ctx, func(ctx context.Context, managerClient manager.ManagerClient) error {
				if createSpec.Ingress == nil {
					request := manager.GetInterceptRequest{Session: session.Info.SessionInfo, Name: args[0]}
					// Throws rpc "not found" error if intercept has not yet been created
					interceptInfo, err := managerClient.GetIntercept(ctx, &request)
					if err != nil {
						return err
					}
					iis, err := session.GetIngressInfos(ctx, &empty.Empty{})
					if err != nil {
						return err
					}
					ingress, err := selectIngress(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), session.Info, interceptInfo.Spec.Agent, interceptInfo.Spec.Namespace, iis.IngressInfos)
					if err != nil {
						return err
					}
					createSpec.Ingress = ingress
				}
				intercept, err := AddPreviewDomain(ctx, reporter,
					clientUpdateInterceptFn(managerClient),
					session.Info.SessionInfo,
					args[0], // intercept name
					&createSpec)
				if err != nil {
					return err
				}
				fmt.Println(cliutil.DescribeIntercepts([]*manager.InterceptInfo{intercept}, nil, false))
				return nil
			})
		},
	}
	AddPreviewFlags("", createCmd.Flags(), &createSpec)

	removeCmd := &cobra.Command{
		Use:  "remove <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Remove a preview domain from an intercept",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cliutil.InitCommand(cmd); err != nil {
				return err
			}
			return cliutil.WithManager(cmd.Context(), func(ctx context.Context, managerClient manager.ManagerClient) error {
				intercept, err := managerClient.UpdateIntercept(ctx, &manager.UpdateInterceptRequest{
					Session: cliutil.GetSession(ctx).Info.SessionInfo,
					Name:    args[0],
					PreviewDomainAction: &manager.UpdateInterceptRequest_RemovePreviewDomain{
						RemovePreviewDomain: true,
					},
				})
				if err != nil {
					return err
				}
				fmt.Println(cliutil.DescribeIntercepts([]*manager.InterceptInfo{intercept}, nil, false))
				return nil
			})
		},
	}

	cmd.AddCommand(createCmd, removeCmd)

	return cmd
}

func selectIngress(
	ctx context.Context,
	in io.Reader,
	out io.Writer,
	connInfo *connector.ConnectInfo,
	interceptName string,
	interceptNamespace string,
	ingressInfos []*manager.IngressInfo,
) (*manager.IngressInfo, error) {
	infos, err := cache.LoadIngressesFromUserCache(ctx)
	if err != nil {
		return nil, err
	}
	key := connInfo.ClusterServer + "/" + connInfo.ClusterContext
	selectOrConfirm := "Confirm"
	cachedIngressInfo := infos[key]
	if cachedIngressInfo == nil {
		iis := ingressInfos
		if len(iis) > 0 {
			cachedIngressInfo = iis[0] // TODO: Better handling when there are several alternatives. Perhaps use SystemA for this?
		} else {
			selectOrConfirm = "Select" // Hard to confirm unless there's a default.
			if interceptNamespace == "" {
				interceptNamespace = "default"
			}
			cachedIngressInfo = &manager.IngressInfo{
				// Default Settings
				Host:   fmt.Sprintf("%s.%s", interceptName, interceptNamespace),
				Port:   80,
				UseTls: false,
			}
		}
	}

	reader := bufio.NewReader(in)

	fmt.Fprintf(out, "\n"+ingressDesc+"\n", selectOrConfirm)
	reply := &manager.IngressInfo{}
	if reply.Host, err = askForHost(ingressQ1, cachedIngressInfo.Host, reader, out); err != nil {
		return nil, err
	}
	if reply.Port, err = askForPortNumber(cachedIngressInfo.Port, reader, out); err != nil {
		return nil, err
	}
	if reply.UseTls, err = askForUseTLS(cachedIngressInfo.UseTls, reader, out); err != nil {
		return nil, err
	}
	if cachedIngressInfo.L5Host == "" {
		cachedIngressInfo.L5Host = reply.Host
	}
	if reply.L5Host, err = askForHost(ingressQ4, cachedIngressInfo.L5Host, reader, out); err != nil {
		return nil, err
	}
	fmt.Fprintln(out)

	if !ingressInfoEqual(cachedIngressInfo, reply) {
		infos[key] = reply
		if err = cache.SaveIngressesToUserCache(ctx, infos); err != nil {
			return nil, err
		}
	}
	return reply, nil
}

func ingressInfoEqual(a, b *manager.IngressInfo) bool {
	return a.Host == b.Host && a.L5Host == b.L5Host && a.Port == b.Port && a.UseTls == b.UseTls
}

const (
	ingressDesc = `To create a preview URL, telepresence needs to know how requests enter
	your cluster.  Please %s the ingress to use.`
	ingressQ1 = `1/4: What's your ingress' IP address?
	 You may use an IP address or a DNS name (this is usually a
	 "service.namespace" DNS name).`
	ingressQ2 = `2/4: What's your ingress' TCP port number?`
	ingressQ3 = `3/4: Does that TCP port on your ingress use TLS (as opposed to cleartext)?`
	ingressQ4 = `4/4: If required by your ingress, specify a different hostname
	 (TLS-SNI, HTTP "Host" header) to be used in requests.`
)

func showPrompt(out io.Writer, question string, defaultValue any) {
	if reflect.ValueOf(defaultValue).IsZero() {
		fmt.Fprintf(out, "\n%s\n\n       [no default]: ", question)
	} else {
		fmt.Fprintf(out, "\n%s\n\n       [default: %v]: ", question, defaultValue)
	}
}

func askForHost(question, cachedHost string, reader *bufio.Reader, out io.Writer) (string, error) {
	for {
		showPrompt(out, question, cachedHost)
		reply, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		reply = strings.TrimSpace(reply)
		if reply == "" {
			if cachedHost == "" {
				continue
			}
			return cachedHost, nil
		}
		if cliutil.HostRx.MatchString(reply) {
			return reply, nil
		}
		fmt.Fprintf(out,
			"Address %q must match the regex [a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)* (e.g. 'myingress.mynamespace')\n",
			reply)
	}
}

func askForPortNumber(cachedPort int32, reader *bufio.Reader, out io.Writer) (int32, error) {
	for {
		showPrompt(out, ingressQ2, cachedPort)
		reply, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		reply = strings.TrimSpace(reply)
		if reply == "" {
			if cachedPort == 0 {
				continue
			}
			return cachedPort, nil
		}
		port, err := strconv.Atoi(reply)
		if err == nil && port > 0 {
			return int32(port), nil
		}
		fmt.Fprintln(out, "port must be a positive integer")
	}
}

func askForUseTLS(cachedUseTLS bool, reader *bufio.Reader, out io.Writer) (bool, error) {
	yn := "n"
	if cachedUseTLS {
		yn = "y"
	}
	showPrompt(out, ingressQ3, yn)
	for {
		reply, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}
		switch strings.TrimSpace(reply) {
		case "":
			return cachedUseTLS, nil
		case "n", "N":
			return false, nil
		case "y", "Y":
			return true, nil
		}
		fmt.Fprintf(out, "       please answer 'y' or 'n'\n       [default: %v]: ", yn)
	}
}
