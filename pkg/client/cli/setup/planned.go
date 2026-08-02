package setup

import (
	"context"
	"fmt"
	"sort"
)

// PlannedObjects renders the embedded chart with the final values and returns
// one "Kind name" line per object the install would create, grouped by kind
// and sorted. Namespaced objects carry their namespace, defaulted to the
// manager namespace exactly like the P1 privilege sweep.
func PlannedObjects(ctx context.Context, managerNamespace string, values map[string]any) ([]string, error) {
	chrt, err := loadEmbeddedChart()
	if err != nil {
		return nil, err
	}
	manifest, err := renderChart(ctx, chrt, managerNamespace, values)
	if err != nil {
		return nil, err
	}
	objs, err := decodeManifests(manifest)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(objs))
	for _, obj := range objs {
		kind := obj.GetKind()
		if clusterScopedKinds[kind] {
			lines = append(lines, fmt.Sprintf("%s %s", kind, obj.GetName()))
			continue
		}
		ns := obj.GetNamespace()
		if ns == "" {
			ns = managerNamespace
		}
		lines = append(lines, fmt.Sprintf("%s %s.%s", kind, obj.GetName(), ns))
	}
	sort.Strings(lines)
	return lines, nil
}
