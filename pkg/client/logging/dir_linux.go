package logging

import (
	"path/filepath"

	"github.com/datawire/telepresence2/v2/pkg/client/cache"
)

func dir() string {
	// We use XDG_CACHE_HOME not XDG_DATA_HOME, because it's always bothered me when
	// things put logs in XDG_DATA_HOME -- XDG_DATA_HOME is for "user-specific data", and
	// XDG_CACHE_HOME is for "user-specific non-essential (cached) data"[1]; logs are
	// non-essential!  A good rule of thumb is: If you track your configuration with Git, and
	// you wouldn't check a given file in to Git (possibly encrypting it before checking it in),
	// then that file either needs to go in XDG_RUNTIME_DIR or XDG_CACHE_DIR; and NOT
	// XDG_DATA_HOME or XDG_CONFIG_HOME.
	//
	// [1]: https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html
	return filepath.Join(cache.CacheDir(), "logs")
}
