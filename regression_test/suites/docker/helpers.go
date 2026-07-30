package docker

import (
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
)

// attachTimeout/attachPollInterval bound how long an intercept takes to show
// up in `list`, shared by every suite in this package that polls for one.
const (
	attachTimeout      = 30 * time.Second
	attachPollInterval = time.Second
)

// present reports whether entries contains a workload named name in ns.
func present(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}

// attached reports whether the workload named name in ns currently carries
// an intercept or ingest.
func attached(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return len(e.InterceptInfo)+len(e.IngestInfo) > 0
		}
	}
	return false
}
