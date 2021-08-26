package scout

import (
	"context"
	"strings"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

func runSwVers(ctx context.Context, versionName string) string {
	cmd := dexec.CommandContext(ctx, "sw_vers", "-"+versionName)
	cmd.DisableLogging = true
	r, err := cmd.Output()
	if err != nil {
		dlog.Warnf(ctx, "Could not get os metadata %s: %v", versionName, err)
		return "unknown"
	}
	return strings.TrimSpace(string(r))
}

func getOsMetadata(ctx context.Context) map[string]interface{} {
	osMeta := map[string]interface{}{
		"os_version":       runSwVers(ctx, "productVersion"),
		"os_build_version": runSwVers(ctx, "buildVersion"),
		"os_name":          runSwVers(ctx, "productName"),
	}
	return osMeta
}
