package runtimeconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixedNamesStayConcise(t *testing.T) {
	names := map[string]string{
		"provider":              ProviderName,
		"splitter":              SplitterName,
		"split label":           SplitLabel,
		"split namespace label": SplitNamespaceLabel,
		"split name label":      SplitNameLabel,
		"namespace label":       NamespaceLabel,
		"active annotation":     ActiveAnnotation,
		"config annotation":     ConfigAnnotation,
		"generation annotation": GenerationAnnotation,
		"healthy annotation":    HealthyAnnotation,
		"split finalizer":       SplitFinalizer,
		"route finalizer":       RouteFinalizer,
	}
	for kind, name := range names {
		require.LessOrEqual(t, len(name), 30, "%s: %s", kind, name)
	}
}
