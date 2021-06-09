package install

import (
	"fmt"

	"github.com/datawire/ambassador/pkg/kates"
)

func GetPodTemplateFromObject(obj kates.Object) (*kates.PodTemplateSpec, error) {
	var tplSpec *kates.PodTemplateSpec
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	switch kind {
	case "ReplicaSet":
		rs := obj.(*kates.ReplicaSet)
		tplSpec = &rs.Spec.Template
	case "Deployment":
		dep := obj.(*kates.Deployment)
		tplSpec = &dep.Spec.Template
	case "StatefulSet":
		statefulSet := obj.(*kates.StatefulSet)
		tplSpec = &statefulSet.Spec.Template
	default:
		return nil, ObjErrorf(obj, "unsupported workload kind %q", kind)
	}

	return tplSpec, nil
}

func ObjErrorf(obj kates.Object, format string, args ...interface{}) error {
	return fmt.Errorf("%s name=%q namespace=%q: %w",
		obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), obj.GetNamespace(),
		fmt.Errorf(format, args...))
}
