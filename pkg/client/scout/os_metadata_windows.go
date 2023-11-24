package scout

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func setOsMetadata(ctx context.Context, osMeta map[string]any) {
	cmd := proc.CommandContext(ctx, "wmic", "os", "get", "Caption,Version,BuildNumber", "/value")
	cmd.DisableLogging = true
	r, err := cmd.Output()
	if err != nil {
		dlog.Warnf(ctx, "Error running wmic: %v", err)
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(r))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, "=")
		if len(parts) < 2 {
			// Untagged string (empty line, etc)
			continue
		}
		name, value := strings.TrimSpace(parts[0]), strings.TrimSpace(strings.Join(parts[1:], "="))
		// systeminfo doesn't have a concept of an OS Build number, so we'll have to set that to unknown
		if name == "Caption" {
			osMeta["os_name"] = value
		}
		if name == "Version" {
			osMeta["os_version"] = value
		}
		if name == "BuildNumber" {
			osMeta["os_build_version"] = value
		}
	}
	if err := scanner.Err(); err != nil {
		dlog.Warnf(ctx, "Unable to scan wmic output: %v", err)
	}
}
