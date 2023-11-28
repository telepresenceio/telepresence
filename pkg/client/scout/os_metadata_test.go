package scout

import (
	"testing"

	"github.com/datawire/dlib/dlog"
)

func TestOsMetadata(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	osMeta := make(map[string]any)
	setOsMetadata(ctx, osMeta)
	for _, k := range []string{"os_version", "os_name"} {
		v := osMeta[k]
		if v == "" || v == "unknown" {
			t.Errorf("Expected %s to be present in os metadata (%v), but it was not", k, osMeta)
		}
	}
	// os_build_version may or may not be present depending on whether the OS reports it, but
	// it should be at least reported as unknown
	if _, ok := osMeta["os_build_version"]; !ok {
		t.Errorf("os_build_version not present in os metadata (%v)", osMeta)
	}
}
