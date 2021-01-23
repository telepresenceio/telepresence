package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/rpc/v2/connector"
	"github.com/datawire/telepresence2/rpc/v2/manager"
	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/client/auth"
	"github.com/datawire/telepresence2/v2/pkg/client/cache"
)

type interceptInfo struct {
	sessionInfo
	name      string
	agentName string
	port      int
	// [REDACTED]
	previewEnabled bool
	previewSpec    manager.PreviewSpec
}

type interceptState struct {
	*interceptInfo
	cs *connectorState
}

func interceptCommand() *cobra.Command {
	ii := &interceptInfo{}
	cmd := &cobra.Command{
		Use:     "intercept [flags] <name> [-- command with arguments...]",
		Short:   "Intercept a service",
		Args:    cobra.MinimumNArgs(1),
		RunE:    ii.intercept,
		PreRunE: updateCheckIfDue,
	}
	flags := cmd.Flags()

	flags.StringVarP(&ii.agentName, "deployment", "d", "", "Name of deployment to intercept, if different from <name>")
	flags.IntVarP(&ii.port, "port", "p", 8080, "Local port to forward to")

	// [REDACTED]

	flags.BoolVarP(&ii.previewEnabled, "preview-url", "u", isLoggedIn(), ``+
		`Generate an edgestack.me preview domain for this intercept. `+
		`(default "true" if you are logged in with 'telepresence login', default "false" otherwise)`,
	)
	addPreviewFlags("preview-url-", flags, &ii.previewSpec)

	return cmd
}

func leaveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "leave <name of intercept>",
		Short: "Remove existing intercept",
		Args:  cobra.ExactArgs(1),
		RunE:  removeIntercept,
	}
}

func (ii *interceptInfo) intercept(cmd *cobra.Command, args []string) error {
	if ii.previewEnabled /* || [REDACTED] */ {
		if err := auth.EnsureLoggedIn(cmd); err != nil {
			return err
		}
	}

	ii.name = args[0]
	args = args[1:]
	if ii.agentName == "" {
		ii.agentName = ii.name
	}
	ii.cmd = cmd
	if len(args) == 0 {
		// start and retain the intercept
		return ii.withConnector(true, func(cs *connectorState) (err error) {
			is := ii.newInterceptState(cs)
			return client.WithEnsuredState(is, true, func() error { return nil })
		})
	}

	// start intercept, run command, then stop the intercept
	return ii.withConnector(false, func(cs *connectorState) error {
		is := ii.newInterceptState(cs)
		return client.WithEnsuredState(is, false, func() error {
			return start(args[0], args[1:], true, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	})
}

// removeIntercept tells the daemon to deactivate and remove an existent intercept
func removeIntercept(cmd *cobra.Command, args []string) error {
	return withStartedConnector(cmd, func(cs *connectorState) error {
		is := &interceptInfo{name: strings.TrimSpace(args[0])}
		return is.newInterceptState(cs).DeactivateState()
	})
}

func (ii *interceptInfo) newInterceptState(cs *connectorState) *interceptState {
	return &interceptState{interceptInfo: ii, cs: cs}
}

func interceptMessage(r *connector.InterceptResult) string {
	msg := ""
	switch r.Error {
	case connector.InterceptError_UNSPECIFIED:
	case connector.InterceptError_NO_PREVIEW_HOST:
		msg = `Your cluster is not configured for Preview URLs.
(Could not find a Host resource that enables Path-type Preview URLs.)
Please specify one or more header matches using --match.`
	case connector.InterceptError_NO_CONNECTION:
		msg = errConnectorIsNotRunning.Error()
	case connector.InterceptError_NO_TRAFFIC_MANAGER:
		msg = "Intercept unavailable: no traffic manager"
	case connector.InterceptError_TRAFFIC_MANAGER_CONNECTING:
		msg = "Connecting to traffic manager..."
	case connector.InterceptError_ALREADY_EXISTS:
		msg = fmt.Sprintf("Intercept with name %q already exists", r.ErrorText)
	case connector.InterceptError_LOCAL_TARGET_IN_USE:
		spec := r.InterceptInfo.Spec
		msg = fmt.Sprintf("Port %s:%d is already in use by intercept %s",
			spec.TargetHost, spec.TargetPort, r.ErrorText)
	case connector.InterceptError_NO_ACCEPTABLE_DEPLOYMENT:
		msg = fmt.Sprintf("No interceptable deployment matching %s found", r.ErrorText)
	case connector.InterceptError_TRAFFIC_MANAGER_ERROR:
		msg = r.ErrorText
	case connector.InterceptError_AMBIGUOUS_MATCH:
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
			fmt.Fprintf(st, "\n%4d: %s on host %s", idx+1, match.Name, match.Hostname)
		}
		msg = st.String()
	case connector.InterceptError_FAILED_TO_ESTABLISH:
		msg = fmt.Sprintf("Failed to establish intercept: %s", r.ErrorText)
	case connector.InterceptError_FAILED_TO_REMOVE:
		msg = fmt.Sprintf("Error while removing intercept: %v", r.ErrorText)
	case connector.InterceptError_NOT_FOUND:
		msg = fmt.Sprintf("Intercept named %q not found", r.ErrorText)
	}
	if id := r.GetInterceptInfo().GetId(); id != "" {
		return fmt.Sprintf("Intercept %q: %s", id, msg)
	} else {
		return fmt.Sprintf("Intercept: %s", msg)
	}
}

func isLoggedIn() bool {
	token, _ := cache.LoadTokenFromUserCache()
	return token != nil
}

func (is *interceptState) EnsureState() (bool, error) {
	// Fill defaults
	if is.name == "" {
		is.name = is.agentName
	}
	// [REDACTED]
	if is.previewEnabled && is.previewSpec.Ingress == nil {
		ingress, err := is.cs.selectIngress(is.cmd.InOrStdin(), is.cmd.OutOrStdout())
		if err != nil {
			return false, err
		}
		is.previewSpec.Ingress = ingress
	}

	// Turn that in to a spec
	spec := &manager.InterceptSpec{
		Name:       is.name,
		Agent:      is.agentName,
		Mechanism:  "tcp",
		TargetHost: "127.0.0.1",
		TargetPort: int32(is.port),
	}
	// [REDACTED]

	// Submit the spec
	r, err := is.cs.connectorClient.CreateIntercept(is.cmd.Context(), &manager.CreateInterceptRequest{
		InterceptSpec: spec,
	})
	if err != nil {
		return false, err
	}
	switch r.Error {
	case connector.InterceptError_UNSPECIFIED:
		fmt.Fprintf(is.cmd.OutOrStdout(), "Using deployment %s\n", spec.Agent)
		var intercept *manager.InterceptInfo
		if is.previewEnabled {
			intercept, err = is.cs.managerClient.UpdateIntercept(is.cmd.Context(), &manager.UpdateInterceptRequest{
				Session: is.cs.info.SessionInfo,
				Name:    spec.Name,
				PreviewDomainAction: &manager.UpdateInterceptRequest_AddPreviewDomain{
					AddPreviewDomain: &is.previewSpec,
				},
			})
			if err != nil {
				err = fmt.Errorf("creating preview domain: %w", err)
				return true, err
			}
		} else {
			intercept = r.InterceptInfo
		}
		fmt.Fprintln(is.cmd.OutOrStdout(), DescribeIntercept(intercept, false))
		return true, nil
	case connector.InterceptError_ALREADY_EXISTS:
		fmt.Fprintln(is.cmd.OutOrStdout(), interceptMessage(r))
		return false, nil
	case connector.InterceptError_NO_CONNECTION:
		return false, errConnectorIsNotRunning
	default:
		return false, errors.New(interceptMessage(r))
	}
}

func (is *interceptState) DeactivateState() error {
	name := strings.TrimSpace(is.name)
	var r *connector.InterceptResult
	var err error
	r, err = is.cs.connectorClient.RemoveIntercept(context.Background(), &manager.RemoveInterceptRequest2{Name: name})
	if err != nil {
		return err
	}
	if r.Error != connector.InterceptError_UNSPECIFIED {
		return errors.New(interceptMessage(r))
	}
	return nil
}

var hostRx = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9\-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9\-]*[a-zA-Z0-9])?)*$`)

func askForHostname(cachedHost string, reader *bufio.Reader, out io.Writer) (string, error) {
	for {
		if cachedHost != "" {
			fmt.Fprintf(out, "Ingress service.namespace [%s] ? ", cachedHost)
		} else {
			fmt.Fprint(out, "Ingress service.namespace ? ")
		}
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
		if hostRx.MatchString(reply) {
			return reply, nil
		}
		fmt.Fprintf(out,
			"Ingress %q must match the regex [a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)* (e.g. 'myingress.mynamespace')\n",
			reply)
	}
}

func askForPortNumber(cachedPort int32, reader *bufio.Reader, out io.Writer) (int32, error) {
	for {
		if cachedPort != 0 {
			fmt.Fprintf(out, "Port [%d] ? ", cachedPort)
		} else {
			fmt.Fprint(out, "Port ? ")
		}
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
	for {
		yn := "n"
		if cachedUseTLS {
			yn = "y"
		}
		fmt.Fprintf(out, "Use TLS y/n [%s] ? ", yn)
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
		fmt.Fprintln(out, "please answer y or n")
	}
}

func (cs *connectorState) selectIngress(in io.Reader, out io.Writer) (*manager.IngressInfo, error) {
	infos, err := cache.LoadIngressesFromUserCache()
	if err != nil {
		return nil, err
	}
	key := cs.info.ClusterServer + "/" + cs.info.ClusterContext
	selectOrConfirm := "Confirm"
	cachedIngressInfo := infos[key]
	if cachedIngressInfo == nil {
		iis := cs.info.IngressInfos
		if len(iis) > 0 {
			cachedIngressInfo = iis[0] // TODO: Better handling when there are several alternatives. Perhaps use SystemA for this?
		} else {
			selectOrConfirm = "Select" // Hard to confirm unless there's a default.
			cachedIngressInfo = &manager.IngressInfo{}
		}
	}

	reader := bufio.NewReader(in)

	fmt.Fprintf(out, "%s the ingress to use for preview URL access\n", selectOrConfirm)
	reply := &manager.IngressInfo{}
	if reply.Host, err = askForHostname(cachedIngressInfo.Host, reader, out); err != nil {
		return nil, err
	}
	if reply.Port, err = askForPortNumber(cachedIngressInfo.Port, reader, out); err != nil {
		return nil, err
	}
	if reply.UseTls, err = askForUseTLS(cachedIngressInfo.UseTls, reader, out); err != nil {
		return nil, err
	}

	if !ingressInfoEqual(cachedIngressInfo, reply) {
		infos[key] = reply
		if err = cache.SaveIngressesToUserCache(infos); err != nil {
			return nil, err
		}
	}
	return reply, nil
}

func ingressInfoEqual(a, b *manager.IngressInfo) bool {
	return a.Host == b.Host && a.Port == b.Port && a.UseTls == b.UseTls
}
