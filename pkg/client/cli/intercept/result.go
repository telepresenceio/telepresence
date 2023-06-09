package intercept

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func Result(r *connector.InterceptResult, err error) error {
	if r == nil || err != nil {
		return err
	}
	msg := ""
	errCat := errcat.Unknown
	switch r.Error {
	case common.InterceptError_UNSPECIFIED:
		return nil
	case common.InterceptError_INTERNAL:
		msg = r.ErrorText
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
			"Cannot create an intercept in namespace %q. A workstation cannot have simultaneous intercepts in different namespaces. Leave all intercepts in namespace %q first.",
			nss[1], nss[0])
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
