package scout

import (
	"bufio"
	"context"
	"os"
	"strings"

	"github.com/datawire/dlib/dlog"
)

func isDocker(ctx context.Context) bool {
	cgroups, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		dlog.Warnf(ctx, "Unable to read /proc/1/cgroup: %v", err)
		return false
	}
	return strings.Contains(string(cgroups), "/docker/")
}

func isWSL(ctx context.Context) bool {
	version, err := os.ReadFile("/proc/version")
	if err != nil {
		dlog.Warnf(ctx, "Unable to read /proc/version: %v", err)
		return false
	}
	v := string(version)
	return strings.Contains(v, "WSL") || strings.Contains(v, "Windows")
}

func setOsMetadata(ctx context.Context, osMeta map[string]any) {
	osMeta["os_docker"] = isDocker(ctx)
	osMeta["os_wsl"] = isWSL(ctx)
	f, err := os.Open("/etc/os-release")
	if os.IsNotExist(err) {
		f, err = os.Open("/usr/lib/os-release")
	}
	if err != nil {
		dlog.Warnf(ctx, "Unable to open /etc/os-release or /usr/lib/os-release: %v", err)
		return
	}
	scanner := bufio.NewScanner(f)
	osRelease := map[string]string{}
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, "=")
		osRelease[parts[0]] = strings.Trim(strings.Join(parts[1:], "="), " \"")
	}
	if err := scanner.Err(); err != nil {
		dlog.Warnf(ctx, "Unable to scan contents of /etc/os-release: %v", err)
		return
	}
	// Different Linuxes will report things in different ways, so this will scan the
	// contents of osRelease and look for each of the different keys that a value might be under
	getFromOSRelease := func(keys ...string) string {
		for _, key := range keys {
			if val, ok := osRelease[key]; ok {
				return val
			}
		}
		return "unknown"
	}
	// ID tends to be cleaner than NAME, and VERSION is more detailed than VERSION_ID
	osMeta["os_name"] = getFromOSRelease("ID", "NAME")
	osMeta["os_version"] = getFromOSRelease("VERSION", "VERSION_ID")
	osMeta["os_build_version"] = getFromOSRelease("BUILD_ID")
}
