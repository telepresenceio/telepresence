package scout

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

func getOsMetadata(ctx context.Context) map[string]interface{} {
	cmd := dexec.CommandContext(ctx, "systeminfo")
	cmd.DisableLogging = true
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}
	r, err := cmd.Output()
	osMeta := map[string]interface{}{}
	if err != nil {
		dlog.Warnf(ctx, "Error running systeminfo: %v", err)
		return osMeta
	}
	scanner := bufio.NewScanner(bytes.NewReader(r))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			// Untagged string (empty line, etc)
			continue
		}
		name, value := strings.TrimSpace(parts[0]), strings.TrimSpace(strings.Join(parts[1:], ":"))
		// systeminfo doesn't have a concept of an OS Build number, so we'll have to set that to unknown
		if name == "OS Name" {
			osMeta["os_name"] = value
		}
		if name == "OS Version" {
			osMeta["os_version"] = value
		}
	}
	osMeta["os_build_version"] = "unknown"
	if err := scanner.Err(); err != nil {
		dlog.Warnf(ctx, "Unable to scan systeminfo output: %v", err)
	}
	return osMeta
}
