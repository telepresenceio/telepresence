package cliutil

import (
	"fmt"
	"net"
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func DescribeIntercepts(iis []*manager.InterceptInfo, volumeMountsPrevented error, debug bool) string {
	sb := strings.Builder{}
	sb.WriteString("intercepted")
	for i, ii := range iis {
		if i > 0 {
			sb.WriteByte('\n')
		}
		describeIntercept(ii, volumeMountsPrevented, debug, &sb)
	}
	return sb.String()
}

func describeIntercept(ii *manager.InterceptInfo, volumeMountsPrevented error, debug bool, sb *strings.Builder) {
	type kv struct {
		Key   string
		Value string
	}

	var fields []kv

	fields = append(fields, kv{"Intercept name", ii.Spec.Name})
	fields = append(fields, kv{"State", func() string {
		msg := ""
		if ii.Disposition > manager.InterceptDispositionType_WAITING {
			msg += "error: "
		}
		msg += ii.Disposition.String()
		if ii.Message != "" {
			msg += ": " + ii.Message
		}
		return msg
	}()})
	fields = append(fields, kv{"Workload kind", ii.Spec.WorkloadKind})

	if debug {
		fields = append(fields, kv{"ID", ii.Id})
	}

	fields = append(fields, kv{"Destination",
		net.JoinHostPort(ii.Spec.TargetHost, fmt.Sprintf("%d", ii.Spec.TargetPort))})

	if ii.Spec.ServicePortIdentifier != "" {
		fields = append(fields, kv{"Service Port Identifier", ii.Spec.ServicePortIdentifier})
	}
	if debug {
		fields = append(fields, kv{"Mechanism", ii.Spec.Mechanism})
		fields = append(fields, kv{"Mechanism Args", fmt.Sprintf("%q", ii.Spec.MechanismArgs)})
		fields = append(fields, kv{"Metadata", fmt.Sprintf("%q", ii.Metadata)})
	}

	if ii.ClientMountPoint != "" {
		fields = append(fields, kv{"Volume Mount Point", ii.ClientMountPoint})
	} else if volumeMountsPrevented != nil {
		fields = append(fields, kv{"Volume Mount Error", volumeMountsPrevented.Error()})
	}

	fields = append(fields, kv{"Intercepting", func() string {
		if ii.MechanismArgsDesc == "" {
			if len(ii.Spec.MechanismArgs) > 0 {
				return fmt.Sprintf("using mechanism=%q with args=%q", ii.Spec.Mechanism, ii.Spec.MechanismArgs)
			}
			return fmt.Sprintf("using mechanism=%q", ii.Spec.Mechanism)
		}
		return ii.MechanismArgsDesc
	}()})

	if ii.PreviewDomain != "" {
		previewURL := ii.PreviewDomain
		// Right now SystemA gives back domains with the leading "https://", but
		// let's not rely on that.
		if !strings.HasPrefix(previewURL, "https://") && !strings.HasPrefix(previewURL, "http://") {
			previewURL = "https://" + previewURL
		}
		fields = append(fields, kv{"Preview URL", previewURL})
	}
	if l5Hostname := ii.GetPreviewSpec().GetIngress().GetL5Host(); l5Hostname != "" {
		fields = append(fields, kv{"Layer 5 Hostname", l5Hostname})
	}

	klen := 0
	for _, kv := range fields {
		if len(kv.Key) > klen {
			klen = len(kv.Key)
		}
	}
	for _, kv := range fields {
		vlines := strings.Split(strings.TrimSpace(kv.Value), "\n")
		fmt.Fprintf(sb, "\n    %-*s: %s", klen, kv.Key, vlines[0])
		for _, vline := range vlines[1:] {
			sb.WriteString("\n      ")
			sb.WriteString(vline)
		}
	}
}
