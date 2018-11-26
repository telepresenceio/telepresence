package watcher

import (
	"fmt"
	"log"

	ms "github.com/mitchellh/mapstructure"
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
		lst := v.([]interface{})
		result = make([]map[string]interface{}, len(lst))
		for idx, obj := range lst {
			result[idx] = obj.(map[string]interface{})
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

func (r Resource) Ready() bool {
	if r.Empty() {
		return false
	}
	kind := r.Kind()
	f, ok := READY[kind]
	if ok {
		return f(r)
	} else {
		log.Printf("warning: don't know how to tell if a %s is ready", kind)
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
