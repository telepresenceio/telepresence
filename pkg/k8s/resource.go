package k8s

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig"
	ms "github.com/mitchellh/mapstructure"
	"gopkg.in/yaml.v2"
)

var READY = map[string]func(Resource) bool{
	"": func(Resource) bool { return false },
	"Deployment": func(r Resource) bool {
		return r.Status().getInt64("readyReplicas") > 0
	},
	"Service": func(r Resource) bool {
		return true
	},
	"Pod": func(r Resource) bool {
		css := r.Status().getMaps("containerStatuses")
		var cs Map
		for _, cs = range css {
			if !cs.getBool("ready") {
				return false
			}
		}
		return true
	},
	"Namespace": func(r Resource) bool {
		return r.Status().getString("phase") == "Active"
	},
	"ServiceAccount": func(r Resource) bool {
		_, ok := r["secrets"]
		return ok
	},
	"ClusterRole": func(r Resource) bool {
		return true
	},
	"ClusterRoleBinding": func(r Resource) bool {
		return true
	},
	"CustomResourceDefinition": func(r Resource) bool {
		conditions := r.Status().getMaps("conditions")
		if len(conditions) > 0 {
			last := conditions[len(conditions)-1]
			if last["status"] == "True" {
				return true
			} else {
				return false
			}
		} else {
			return false
		}
	},
}

type Map map[string]interface{}

func (m Map) getMap(name string) map[string]interface{} {
	v, ok := m[name]
	if ok {
		return v.(map[string]interface{})
	} else {
		return Map{}
	}
}

func (m Map) getMaps(name string) []map[string]interface{} {
	var result []map[string]interface{}
	v, ok := m[name]
	if ok {
		lst, ok := v.([]interface{})
		if ok {
			result = make([]map[string]interface{}, len(lst))
			for idx, obj := range lst {
				result[idx] = obj.(map[string]interface{})
			}
		}
	}
	return result
}

func (m Map) getString(key string) string {
	v, ok := m[key]
	if ok {
		return v.(string)
	} else {
		return ""
	}
}

func (m Map) getInt64(key string) int64 {
	v, ok := m[key]
	if ok {
		return v.(int64)
	} else {
		return 0
	}
}

func (m Map) getBool(key string) bool {
	v, ok := m[key]
	if ok {
		return v.(bool)
	} else {
		return false
	}
}

type Resource map[string]interface{}

func (r Resource) Kind() string {
	k, ok := r["kind"]
	if ok {
		return k.(string)
	} else {
		return ""
	}
}

func (r Resource) Empty() bool {
	_, ok := r["kind"]
	return !ok
}

func (r Resource) Status() Map {
	return Map(r).getMap("status")
}

func (r Resource) Spec() Map {
	return Map(r).getMap("spec")
}

func (r Resource) ReadyImplemented() bool {
	if r.Empty() {
		return false
	}
	kind := r.Kind()
	_, ok := READY[kind]
	return ok
}

func (r Resource) Ready() bool {
	if r.Empty() {
		return false
	}
	kind := r.Kind()
	f, ok := READY[kind]
	if ok {
		return f(r)
	} else {
		return true
	}
}

type Metadata map[string]interface{}

func (r Resource) Metadata() Metadata {
	return Metadata(Map(r).getMap("metadata"))
}

func (m Metadata) Name() string { return Map(m).getString("name") }
func (r Resource) Name() string { return r.Metadata().Name() }

func (m Metadata) Namespace() string { return Map(m).getString("namespace") }
func (r Resource) Namespace() string { return r.Metadata().Namespace() }

func (m Metadata) ResourceVersion() string { return Map(m).getString("resourceVersion") }
func (r Resource) ResourceVersion() string { return r.Metadata().ResourceVersion() }

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
		result := make([]interface{}, len(obj))
		for i, v := range obj {
			result[i] = fixup(v)
		}
		return result
	case map[interface{}]interface{}:
		result := make(map[string]interface{})
		for k, v := range obj {
			switch k := k.(type) {
			case string:
				result[k] = fixup(v)
			}
		}
		return result
	default:
		return obj
	}
}

func NewResourceFromYaml(yaml map[interface{}]interface{}) Resource {
	return Resource(fixup(yaml).(map[string]interface{}))
}

func isTemplate(input []byte) bool {
	return strings.Contains(string(input), "@TEMPLATE@")
}

func ExpandResource(path string) (result []byte, err error) {
	input, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if isTemplate(input) {
		tmpl := template.New(filepath.Base(path)).Funcs(sprig.TxtFuncMap())
		_, err := tmpl.Parse(string(input))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}

		buf := bytes.NewBuffer(nil)
		err = tmpl.ExecuteTemplate(buf, filepath.Base(path), nil)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}

		result = buf.Bytes()
	} else {
		result = input
	}

	return
}

func LoadResources(path string) (result []Resource, err error) {
	var input []byte
	input, err = ExpandResource(path)
	if err != nil {
		return
	}
	d := yaml.NewDecoder(bytes.NewReader(input))
	for {
		var uns map[interface{}]interface{}
		err = d.Decode(&uns)
		if err != nil {
			if err == io.EOF {
				err = nil
			} else {
				err = fmt.Errorf("%s: %v", path, err)
			}
			return
		}
		res := NewResourceFromYaml(uns)
		result = append(result, res)
	}
}

func SaveResources(path string, resources []Resource) error {
	output, err := MarshalResources(resources)
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	err = ioutil.WriteFile(path, output, 0644)
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	return nil
}

func WalkResources(filter func(name string) bool, roots ...string) (result []Resource, err error) {
	for _, root := range roots {
		err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if !info.IsDir() && filter(path) {
				rsrcs, err := LoadResources(path)
				if err != nil {
					return err
				} else {
					result = append(result, rsrcs...)
				}
			}

			return nil
		})
		if err != nil {
			return
		}
	}

	return
}

func MarshalResources(resources []Resource) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	e := yaml.NewEncoder(buf)
	for _, r := range resources {
		err := e.Encode(r)
		if err != nil {
			return nil, err
		}
	}
	e.Close()
	return buf.Bytes(), nil
}

func MarshalResource(resource Resource) ([]byte, error) {
	return MarshalResources([]Resource{resource})
}
