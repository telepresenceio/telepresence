package scout

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

func setOsMetadata(ctx context.Context, osMeta map[string]any) {
	osMeta["os_version"] = "unknown"
	osMeta["os_build_version"] = "unknown"
	osMeta["os_name"] = "unknown"
	cmd := dexec.CommandContext(ctx, "sw_vers")
	cmd.DisableLogging = true
	if r, err := cmd.Output(); err != nil {
		dlog.Warnf(ctx, "Could not get os metadata: %v", err)
	} else {
		sc := bufio.NewScanner(bytes.NewReader(r))
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) == 2 {
				switch fs[0] {
				case "ProductName:":
					osMeta["os_name"] = fs[1]
				case "ProductVersion:":
					osMeta["os_version"] = fs[1]
				case "BuildVersion:":
					osMeta["os_build_version"] = fs[1]
				}
			}
		}
	}
}
