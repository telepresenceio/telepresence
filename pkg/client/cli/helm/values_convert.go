package helm

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/charts"
)

// ValuesFromMap converts a map Helm produced (parsed --set/-f input, or a stored
// release's Config) into Values. A key the struct has no field for is an error.
func ValuesFromMap(m map[string]any) (*Values, error) {
	if err := checkKnownKeys("", m, reflect.TypeOf(Values{})); err != nil {
		return nil, err
	}
	v := &Values{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, v); err != nil {
		return nil, fmt.Errorf("unable to convert map to Values: %w", err)
	}
	return v, nil
}

// ToMap converts v into the map[string]any shape Helm's install, upgrade and
// template actions require.
func (v *Values) ToMap() (map[string]any, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(v)
	if err != nil {
		return nil, fmt.Errorf("unable to convert Values to map: %w", err)
	}
	return m, nil
}

// ParseValues decodes YAML (JSON is valid YAML) directly into Values, rejecting
// any key the struct has no field for.
func ParseValues(data []byte) (*Values, error) {
	v := &Values{}
	if err := yaml.UnmarshalStrict(data, v); err != nil {
		return nil, fmt.Errorf("unable to parse values: %w", err)
	}
	return v, nil
}

//nolint:gochecknoglobals // memoizes the embedded chart's own values.yaml
var (
	defaultValuesOnce sync.Once
	defaultValues     *Values
	errDefaultValues  error
)

// DefaultValues returns the telepresence-oss chart's own values.yaml, decoded once
// and copied fresh for every caller so none can mutate the cached values.
func DefaultValues() (*Values, error) {
	defaultValuesOnce.Do(func() {
		data, err := charts.TelepresenceFS.ReadFile(charts.TelepresenceChartName + "/values.yaml")
		if err != nil {
			errDefaultValues = err
			return
		}
		defaultValues, errDefaultValues = ParseValues(data)
	})
	if errDefaultValues != nil {
		return nil, errDefaultValues
	}
	return defaultValues.DeepCopy(), nil
}

// jsonName returns the json tag name of f, or its Go name when the tag has none.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}
	return name
}

// jsonFields maps t's json tag names to their field types.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		fields[jsonName(t.Field(i))] = t.Field(i).Type
	}
	return fields
}

// checkKnownKeys reports an error naming the first key in m, at path, that t (or,
// for a nested map, the struct type of the matching field) does not declare.
func checkKnownKeys(path string, m map[string]any, t reflect.Type) error {
	fields := jsonFields(t)
	for key, val := range m {
		fp := key
		if path != "" {
			fp = path + "." + key
		}
		ft, ok := fields[key]
		if !ok {
			return fmt.Errorf("%q is not a chart value or client setting", fp)
		}
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() == pkgPath {
			if nested, ok := val.(map[string]any); ok {
				if err := checkKnownKeys(fp, nested, ft); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
