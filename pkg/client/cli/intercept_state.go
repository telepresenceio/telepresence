package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	manager "github.com/datawire/telepresence2/pkg/rpc"
	"github.com/datawire/telepresence2/pkg/rpc/connector"
)

type interceptState struct {
	cmd *cobra.Command
	cs  *connectorState
	ir  *manager.CreateInterceptRequest
}

func newInterceptState(cs *connectorState, ir *manager.CreateInterceptRequest, cmd *cobra.Command) *interceptState {
	return &interceptState{cs: cs, ir: ir, cmd: cmd}
}

func interceptMessage(ie connector.InterceptError, txt string) string {
	msg := ""
	switch ie {
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
		msg = fmt.Sprintf("Intercept with name %q already exists", txt)
	case connector.InterceptError_NO_ACCEPTABLE_DEPLOYMENT:
		msg = fmt.Sprintf("No interceptable deployment matching %s found", txt)
	case connector.InterceptError_TRAFFIC_MANAGER_ERROR:
		msg = txt
	case connector.InterceptError_AMBIGUOUS_MATCH:
		var matches []manager.AgentInfo
		err := json.Unmarshal([]byte(txt), &matches)
		if err != nil {
			msg = fmt.Sprintf("Unable to unmarshal JSON: %s", err.Error())
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
		msg = fmt.Sprintf("Failed to establish intercept: %s", txt)
	case connector.InterceptError_FAILED_TO_REMOVE:
		msg = fmt.Sprintf("Error while removing intercept: %v", txt)
	case connector.InterceptError_NOT_FOUND:
		msg = fmt.Sprintf("Intercept named %q not found", txt)
	}
	return msg
}

func prepareIntercept(ii *manager.CreateInterceptRequest) error {
	var host, portStr string
	spec := ii.InterceptSpec
	hp := strings.SplitN(spec.TargetHost, ":", 2)
	if len(hp) < 2 {
		portStr = hp[0]
	} else {
		host = strings.TrimSpace(hp[0])
		portStr = hp[1]
	}
	if len(host) == 0 {
		host = "127.0.0.1"
	}
	port, err := strconv.ParseInt(portStr, 10, 32)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("Failed to parse %q as HOST:PORT: %v\n", spec.TargetHost, err))
	}
	spec.TargetHost = host
	spec.TargetPort = int32(port)
	return nil
}

func (is *interceptState) EnsureState() (bool, error) {
	r, err := is.cs.grpc.CreateIntercept(is.cmd.Context(), is.ir)
	if err != nil {
		return false, err
	}
	switch r.Error {
	case connector.InterceptError_UNSPECIFIED:
		fmt.Fprintf(is.cmd.OutOrStdout(), "Using deployment %s\n", is.ir.InterceptSpec.Name)
		return true, nil
	case connector.InterceptError_ALREADY_EXISTS:
		fmt.Fprintln(is.cmd.OutOrStdout(), interceptMessage(r.Error, r.ErrorText))
		return false, nil
	case connector.InterceptError_NO_CONNECTION:
		return false, errConnectorIsNotRunning
	}
	return false, errors.New(interceptMessage(r.Error, r.ErrorText))
}

func (is *interceptState) DeactivateState() error {
	name := strings.TrimSpace(is.ir.InterceptSpec.Name)
	var r *connector.InterceptResult
	var err error
	r, err = is.cs.grpc.RemoveIntercept(context.Background(), &manager.RemoveInterceptRequest2{Name: name})
	if err != nil {
		return err
	}
	if r.Error != connector.InterceptError_UNSPECIFIED {
		return errors.New(interceptMessage(r.Error, r.ErrorText))
	}
	return nil
}
