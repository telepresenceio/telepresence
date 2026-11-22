package setup

import (
	"context"
	"fmt"
	"sort"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/rbacgen"
)

// PlannedObjects renders the embedded chart with the final values and returns
// one "Kind name" line per object the install would create, grouped by kind
// and sorted. Namespaced objects carry their namespace, defaulted to the
// manager namespace exactly like the P1 privilege sweep.
func PlannedObjects(ctx context.Context, managerNamespace string, values *helm.Values) ([]string, error) {
	render, err := rbacgen.RenderChart(ctx, managerNamespace, values)
	if err != nil {
		return nil, err
	}
	objs := render.All()
	lines := make([]string, 0, len(objs))
	for _, obj := range objs {
		if ns := rbacgen.ObjectNamespace(obj, managerNamespace); ns != "" {
			lines = append(lines, fmt.Sprintf("%s %s.%s", obj.GetKind(), obj.GetName(), ns))
		} else {
			lines = append(lines, fmt.Sprintf("%s %s", obj.GetKind(), obj.GetName()))
		}
	}
	sort.Strings(lines)
	return lines, nil
}
