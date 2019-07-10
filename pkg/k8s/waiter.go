package k8s

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/datawire/teleproxy/pkg/tpu"
)

type Waiter struct {
	watcher *Watcher
	kinds   map[string]map[string]bool
}

// NewWaiter constructs a Waiter object based on the suppliec Watcher.
func NewWaiter(watcher *Watcher) (w *Waiter, err error) {
	if watcher == nil {
		cli, err := NewClient(nil)
		if err != nil {
			return nil, err
		}
		watcher = cli.Watcher()
	}
	return &Waiter{
		watcher: watcher,
		kinds:   make(map[string]map[string]bool),
	}, nil
}

// canonical returns the canonical form of either a resource name or a
// resource type name:
//
//   ResourceName: TYPE/NAME[.NAMESPACE]
//   ResourceType: TYPE
//
func (w *Waiter) canonical(name string) string {
	parts := strings.Split(name, "/")

	var kind string
	switch len(parts) {
	case 1:
		kind = parts[0]
		name = ""
	case 2:
		kind = parts[0]
		name = parts[1]
	default:
		return ""
	}

	ri, err := w.watcher.client.ResolveResourceType(kind)
	if err != nil {
		panic(fmt.Sprintf("%s: %v", kind, err))
	}
	kind = strings.ToLower(ri.String())

	if name == "" {
		return kind
	}

	if ri.Namespaced {
		var namespace string

		parts = strings.Split(name, ".")
		switch len(parts) {
		case 1:
			namespace = w.watcher.client.namespace
		case 2:
			name = parts[0]
			namespace = parts[1]
		default:
			return ""
		}

		return fmt.Sprintf("%s/%s.%s", kind, name, namespace)
	}

	return fmt.Sprintf("%s/%s", kind, name)
}

func (w *Waiter) Add(resource string) error {
	cresource := w.canonical(resource)

	parts := strings.Split(cresource, "/")
	if len(parts) != 2 {
		return fmt.Errorf("expecting <kind>/<name>[.<namespace>], got %s", resource)
	}

	kind := parts[0]
	name := parts[1]

	resources, ok := w.kinds[kind]
	if !ok {
		resources = make(map[string]bool)
		w.kinds[kind] = resources
	}
	resources[name] = false
	return nil
}

func (w *Waiter) Scan(path string) (err error) {
	resources, err := LoadResources(path)
	for _, res := range resources {
		err = w.Add(fmt.Sprintf("%s/%s", res.QKind(), res.QName()))
		if err != nil {
			return
		}
	}
	return
}

func (w *Waiter) ScanPaths(files []string) (err error) {
	resources, err := WalkResources(tpu.IsYaml, files...)
	for _, res := range resources {
		err = w.Add(fmt.Sprintf("%s/%s", res.QKind(), res.QName()))
		if err != nil {
			return
		}
	}
	return
}

func (w *Waiter) remove(kind, name string) {
	delete(w.kinds[kind], name)
}

func (w *Waiter) isEmpty() bool {
	for _, names := range w.kinds {
		if len(names) > 0 {
			return false
		}
	}

	return true
}

func (w *Waiter) Wait(timeout time.Duration) bool {
	start := time.Now()
	printed := make(map[string]bool)
	w.watcher.Watch("events", func(watcher *Watcher) {
		for _, r := range watcher.List("events") {
			if lastStr, ok := r["lastTimestamp"].(string); ok {
				last, err := time.Parse("2006-01-02T15:04:05Z", lastStr)
				if err != nil {
					log.Println(err)
					continue
				}
				if last.Before(start) {
					continue
				}
			}
			if !printed[r.QName()] {
				var name string
				if obj, ok := r["involvedObject"].(map[string]interface{}); ok {
					name = w.canonical(fmt.Sprintf("%s/%v.%v", Resource(obj).QKind(), obj["name"], obj["namespace"]))
				} else {
					name = r.QName()
				}
				fmt.Printf("event: %s %s\n", name, r["message"])
				printed[r.QName()] = true
			}
		}
	})

	listener := func(watcher *Watcher) {
		for kind, names := range w.kinds {
			for name := range names {
				r := watcher.Get(kind, name)
				if r.Ready() {
					if r.ReadyImplemented() {
						fmt.Printf("ready: %s/%s\n", w.canonical(r.QKind()), r.QName())
					} else {
						fmt.Printf("ready: %s/%s (UNIMPLEMENTED)\n",
							w.canonical(r.QKind()), r.QName())
					}
					w.remove(kind, name)
				}
			}
		}

		if w.isEmpty() {
			watcher.Stop()
		}
	}

	for k := range w.kinds {
		err := w.watcher.Watch(k, listener)
		if err != nil {
			panic(err)
		}
	}

	w.watcher.Start()

	go func() {
		time.Sleep(timeout)
		w.watcher.Stop()
	}()

	w.watcher.Wait()

	result := true

	for kind, names := range w.kinds {
		for name := range names {
			fmt.Printf("not ready: %s/%s\n", kind, name)
			result = false
		}
	}

	return result
}
