package intercept

import (
	"context"
	"strings"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ingest"
)

func DescribeIntercepts(
	ctx context.Context,
	iis []*manager.InterceptInfo,
	igs []*rpc.IngestInfo,
	services []*manager.ServiceAssociation,
	volumeMountsPrevented error,
	debug bool,
) string {
	sb := strings.Builder{}
	if len(iis) > 0 {
		var nis, ris, wts []*manager.InterceptInfo
		for _, ii := range iis {
			switch {
			case ii.Spec.Replace:
				ris = append(ris, ii)
			case ii.Spec.Wiretap:
				wts = append(wts, ii)
			default:
				nis = append(nis, ii)
			}
		}
		if len(ris) > 0 {
			sb.WriteString("replaced")
			for _, ii := range ris {
				sb.WriteByte('\n')
				describeIntercept(ctx, ii, services, volumeMountsPrevented, debug, &sb)
			}
		}
		if len(nis) > 0 {
			sb.WriteString("intercepted")
			for _, ii := range nis {
				sb.WriteByte('\n')
				describeIntercept(ctx, ii, services, volumeMountsPrevented, debug, &sb)
			}
		}
		if len(wts) > 0 {
			sb.WriteString("wiretapped")
			for _, ii := range wts {
				sb.WriteByte('\n')
				describeIntercept(ctx, ii, services, volumeMountsPrevented, debug, &sb)
			}
		}
	}
	if len(igs) > 0 {
		sb.WriteString("ingested")
		for _, ig := range igs {
			sb.WriteByte('\n')
			describeIngest(ctx, ig, volumeMountsPrevented, &sb)
		}
	}
	return sb.String()
}

func describeIntercept(
	ctx context.Context,
	ii *manager.InterceptInfo,
	services []*manager.ServiceAssociation,
	volumeMountsPrevented error,
	debug bool,
	sb *strings.Builder,
) {
	info := NewInfo(ctx, ii, false, volumeMountsPrevented)
	info.debug = debug
	info.AccessURLs = routeURLs(routesForService(ii.Spec, services))
	_, _ = info.WriteTo(sb)
}

func describeIngest(ctx context.Context, ig *rpc.IngestInfo, volumeMountsPrevented error, sb *strings.Builder) {
	info := ingest.NewInfo(ctx, ig, volumeMountsPrevented)
	_, _ = info.WriteTo(sb)
}
