package util

import (
	"fmt"
	"net"
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func DescribeIntercepts(iis []*manager.InterceptInfo, volumeMountsPrevented error, debug bool) string {
	sb := strings.Builder{}
	sb.WriteString("intercepted")
	for _, ii := range iis {
		sb.WriteByte('\n')
		describeIntercept(ii, volumeMountsPrevented, debug, &sb)
	}
	return sb.String()
}

func describeIntercept(ii *manager.InterceptInfo, volumeMountsPrevented error, debug bool, sb *strings.Builder) {
	kvf := ioutil.DefaultKeyValueFormatter()
	kvf.Prefix = "   "
	kvf.Add("Intercept name", ii.Spec.Name)
	kvf.Add("State", func() string {
		msg := ""
		if ii.Disposition > manager.InterceptDispositionType_WAITING {
			msg += "error: "
		}
		msg += ii.Disposition.String()
		if ii.Message != "" {
			msg += ": " + ii.Message
		}
		return msg
	}())
	kvf.Add("Workload kind", ii.Spec.WorkloadKind)

	if debug {
		kvf.Add("ID", ii.Id)
	}

	kvf.Add(
		"Destination",
		net.JoinHostPort(ii.Spec.TargetHost, fmt.Sprintf("%d", ii.Spec.TargetPort)),
	)

	if ii.Spec.ServicePortIdentifier != "" {
		kvf.Add("Service Port Identifier", ii.Spec.ServicePortIdentifier)
	}
	if debug {
		kvf.Add("Mechanism", ii.Spec.Mechanism)
		kvf.Add("Mechanism Args", fmt.Sprintf("%q", ii.Spec.MechanismArgs))
		kvf.Add("Metadata", fmt.Sprintf("%q", ii.Metadata))
	}

	kvf.Add("Http-Headers", fmt.Sprintf("%q", ii.Spec.HttpHeaders))

	if ii.ClientMountPoint != "" {
		kvf.Add("Volume Mount Point", ii.ClientMountPoint)
	} else if volumeMountsPrevented != nil {
		kvf.Add("Volume Mount Error", volumeMountsPrevented.Error())
	}

	kvf.Add("Intercepting", func() string {
		if ii.MechanismArgsDesc == "" {
			if len(ii.Spec.MechanismArgs) > 0 {
				return fmt.Sprintf("using mechanism=%q with args=%q", ii.Spec.Mechanism, ii.Spec.MechanismArgs)
			}
			return fmt.Sprintf("using mechanism=%q", ii.Spec.Mechanism)
		}
		return ii.MechanismArgsDesc
	}())

	if ii.PreviewDomain != "" {
		previewURL := ii.PreviewDomain
		// Right now SystemA gives back domains with the leading "https://", but
		// let's not rely on that.
		if !strings.HasPrefix(previewURL, "https://") && !strings.HasPrefix(previewURL, "http://") {
			previewURL = "https://" + previewURL
		}
		kvf.Add("Preview URL", previewURL)
	}
	if l5Hostname := ii.GetPreviewSpec().GetIngress().GetL5Host(); l5Hostname != "" {
		kvf.Add("Layer 5 Hostname", l5Hostname)
	}
	_, _ = kvf.WriteTo(sb)
}
