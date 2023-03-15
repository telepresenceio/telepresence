package intercept

import (
	"context"
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func DescribeIntercepts(ctx context.Context, iis []*manager.InterceptInfo, volumeMountsPrevented string, debug bool) string {
	sb := strings.Builder{}
	sb.WriteString("intercepted")
	for _, ii := range iis {
		sb.WriteByte('\n')
		describeIntercept(ctx, ii, volumeMountsPrevented, debug, &sb)
	}
	return sb.String()
}

func describeIntercept(ctx context.Context, ii *manager.InterceptInfo, volumeMountsPrevented string, debug bool, sb *strings.Builder) {
	info := NewInfo(ctx, ii, volumeMountsPrevented)
	info.debug = debug
	_, _ = info.WriteTo(sb)
}
