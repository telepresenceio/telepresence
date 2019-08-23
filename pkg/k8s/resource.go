package k8s

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	ms "github.com/mitchellh/mapstructure"
	"gopkg.in/yaml.v2"
)

// Map is a YAML/JSON-ish map with some convenience methods on it.
type Map map[string]interface{}

// GetMap returns m[name], type-asserted to be a map.  If m[name] is
// not set, or is not a map, then an non-nil empty map is returned.
//
// That is: it always safely returns a usable map.
func (m Map) GetMap(name string) map[string]interface{} {
	v, ok := m[name].(map[string]interface{})
	if !ok {
		return map[string]interface{}{}
	}
	return v
}

// GetMaps returns m[name], type-asserted to be a list of maps.  If
// m[name] is not set, or is not a list of maps, then a nil array is
// returned.
//
// That is: it always safely returns a usable slice of maps.
func (m Map) GetMaps(name string) []map[string]interface{} {
	v, ok := m[name].([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, len(v))
	for idx, obj := range v {
		result[idx], ok = obj.(map[string]interface{})
		if !ok {
			result[idx] = map[string]interface{}{}
		}
	}
	return result
}

// GetString returns m[key], type-asserted to be a string.  If m[key]
// is not set, or it is not a string, then an empty string is
// returned.
//
// That is: it always safely returns a usable string.
func (m Map) GetString(key string) string {
	v, ok := m[key].(string)
	if !ok {
		return ""
	}
	return v
}

// GetInt64 returns m[key], type-asserted to be an int64.  If m[key]
// is not set, or it is not an int64, then 0 is returned.
//
// That is: it always safely returns a usable int64.
func (m Map) GetInt64(key string) int64 {
	v, ok := m[key].(int64)
	if !ok {
		return 0
	}
	return v
}

// GetBool returns m[key], type-asserted to be a bool.  If m[key] is
// not set, or it is not a bool, then false is returned.
//
// That is: it always safely returns a usable bool.
func (m Map) GetBool(key string) bool {
	v, ok := m[key].(bool)
	if !ok {
		return false
	}
	return v
}

// Resource is map from strings to any with some convenience methods for
// accessing typical Kubernetes resource fields.
type Resource map[string]interface{}

func (r Resource) Kind() string {
	return Map(r).GetString("kind")
}

// QKind returns a fully qualified resource kind with the following
// format: <kind>.<version>.<group>
func (r Resource) QKind() string {
	gv := Map(r).GetString("apiVersion")
	k := Map(r).GetString("kind")

	var g, v string
	if slash := strings.IndexByte(gv, '/'); slash < 0 {
		g = ""
		v = gv
	} else {
		g = gv[:slash]
		v = gv[slash+1:]
	}

	return strings.Join([]string{k, v, g}, ".")
}

func (r Resource) Empty() bool {
	_, ok := r["kind"]
	return !ok
}

func (r Resource) Status() Map {
	return Map(r).GetMap("status")
}

func (r Resource) Data() Map {
	return Map(r).GetMap("data")
}

func (r Resource) Spec() Map {
	return Map(r).GetMap("spec")
}

type Metadata map[string]interface{}

func (r Resource) Metadata() Metadata {
	return Metadata(Map(r).GetMap("metadata"))
}

// Name returns the metadata "name".
func (m Metadata) Name() string { return Map(m).GetString("name") }
func (r Resource) Name() string { return r.Metadata().Name() }

// Namespace returns the metadata "namespace".
func (m Metadata) Namespace() string { return Map(m).GetString("namespace") }
func (r Resource) Namespace() string { return r.Metadata().Namespace() }

// ResourceVersion returns the metadata "resourceVersion".
func (m Metadata) ResourceVersion() string { return Map(m).GetString("resourceVersion") }
func (r Resource) ResourceVersion() string { return r.Metadata().ResourceVersion() }

func (m Metadata) Annotations() map[string]interface{} {
	return Map(m).GetMap("annotations")
}

func (m Metadata) QName() string {
	ns := m.Namespace()
	if ns == "" {
		return m.Name()
	} else {
		return fmt.Sprintf("%s.%s", m.Name(), ns)
	}
}

func (r Resource) QName() string { return r.Metadata().QName() }

func (r Resource) Decode(output interface{}) error {
	return ms.Decode(r, output)
}

// This fixes objects parsed by yaml to objects that are compatible
// with json by converting any map[interface{}]interface{} to
// map[string]interface{}
func fixup(obj interface{}) interface{} {
	switch obj := obj.(type) {
	case []interface{}:
		return fixupList(obj)
	case map[interface{}]interface{}:
		return fixupMap(obj)
	default:
		return obj
	}
}

func fixupList(obj []interface{}) []interface{} {
	result := make([]interface{}, len(obj))
	for i, v := range obj {
		result[i] = fixup(v)
	}
	return result
}

func fixupMap(obj map[interface{}]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for key, val := range obj {
		if key, ok := key.(string); ok {
			result[key] = fixup(val)
		}
	}
	return result
}

// NewResourceFromYaml takes a (already-parsed) untyped YAML
// structure, and fixes it up to be JSON-compatible, and returns it as
// a Resource.
func NewResourceFromYaml(yaml map[interface{}]interface{}) Resource {
	return Resource(fixupMap(yaml))
}

func ParseResources(name, input string) (result []Resource, err error) {
	d := yaml.NewDecoder(bytes.NewReader([]byte(input)))
	for {
		var uns map[interface{}]interface{}
		err = d.Decode(&uns)
		if err != nil {
			if err == io.EOF {
				err = nil
			} else {
				err = fmt.Errorf("%s: %v", name, err)
			}
			return
		}
		res := NewResourceFromYaml(uns)
		result = append(result, res)
	}
}
