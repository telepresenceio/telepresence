package k8s

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig"
	ms "github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"

	"github.com/datawire/teleproxy/pkg/supervisor"
)

var READY = map[string]func(Resource) bool{
	"": func(Resource) bool { return false },
	"Deployment": func(r Resource) bool {
		// NOTE - plombardi - (2019-05-20)
		// a zero-sized deployment never gets status.readyReplicas and friends set by kubernetes deployment controller.
		// this effectively short-circuits the wait.
		//
		// in the future it might be worth porting this change to StatefulSets, ReplicaSets and ReplicationControllers
		if r.Spec().GetInt64("replicas") == 0 {
			return true
		}

		return r.Status().GetInt64("readyReplicas") > 0
	},
	"Service": func(r Resource) bool {
		return true
	},
	"Pod": func(r Resource) bool {
		css := r.Status().GetMaps("containerStatuses")
		var cs Map
		for _, cs = range css {
			if !cs.GetBool("ready") {
				return false
			}
		}
		return true
	},
	"Namespace": func(r Resource) bool {
		return r.Status().GetString("phase") == "Active"
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
		conditions := r.Status().GetMaps("conditions")
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

func NewResourceFromYaml(yaml map[interface{}]interface{}) Resource {
	return Resource(fixupMap(yaml))
}

func isTemplate(input []byte) bool {
	return strings.Contains(string(input), "@TEMPLATE@")
}

func builder(dir string) func(string) (string, error) {
	return func(dockerfile string) (string, error) {
		return image(dir, dockerfile)
	}
}

func image(dir, dockerfile string) (string, error) {
	var result string
	errs := supervisor.Run("BLD", func(p *supervisor.Process) error {
		iidfile, err := ioutil.TempFile("", "iid")
		if err != nil {
			return err
		}
		defer os.Remove(iidfile.Name())
		err = iidfile.Close()
		if err != nil {
			return err
		}

		ctx := filepath.Dir(filepath.Join(dir, dockerfile))
		cmd := p.Command("docker", "build", "-f", filepath.Base(dockerfile), ".", "--iidfile", iidfile.Name())
		cmd.Dir = ctx
		err = cmd.Run()
		if err != nil {
			return err
		}
		content, err := ioutil.ReadFile(iidfile.Name())
		if err != nil {
			return err
		}
		iid := strings.Split(strings.TrimSpace(string(content)), ":")[1]
		short := iid[:12]

		registry := strings.TrimSpace(os.Getenv("DOCKER_REGISTRY"))
		if registry == "" {
			return errors.Errorf("please set the DOCKER_REGISTRY environment variable")
		}
		tag := fmt.Sprintf("%s/%s", registry, short)

		cmd = p.Command("docker", "tag", iid, tag)
		err = cmd.Run()
		if err != nil {
			return err
		}

		result = tag

		cmd = p.Command("docker", "push", tag)
		return cmd.Run()
	})
	if len(errs) > 0 {
		return "", errors.Errorf("errors building %s: %v", dockerfile, errs)
	}
	return result, nil
}

func ExpandResource(path string) (result []byte, err error) {
	input, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if isTemplate(input) {
		funcs := sprig.TxtFuncMap()
		funcs["image"] = builder(filepath.Dir(path))
		tmpl := template.New(filepath.Base(path)).Funcs(funcs)
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

func LoadResources(path string) (result []Resource, err error) {
	var input []byte
	input, err = ExpandResource(path)
	if err != nil {
		return
	}
	result, err = ParseResources(path, string(input))
	return
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

//func (r *Resource) MarshalJSON() ([]byte, error) {
//	return json.Marshal(r)
//}
//
//func (r *Resource) UnmarshalJSON(data []byte) error {
//	return json.Unmarshal(data, r)
//}

//func (r Resource) MarshalJSON() ([]byte, error) {
//	return json.Marshal(r)
//}
//
//func (r Resource) UnmarshalJSON(data []byte) error {
//	return json.Unmarshal(data, &Resource{})
//}

func MarshalResource(resource Resource) ([]byte, error) {
	return MarshalResources([]Resource{resource})
}

func MarshalResourcesJSON(resources []Resource) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	e := json.NewEncoder(buf)
	for _, r := range resources {
		err := e.Encode(r)
		if err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

func MarshalResourceJSON(resource Resource) ([]byte, error) {
	return MarshalResourcesJSON([]Resource{resource})
}
