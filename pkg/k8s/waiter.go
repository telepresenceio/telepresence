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

func NewWaiter(watcher *Watcher) (w *Waiter) {
	if watcher == nil {
		watcher = NewClient(nil).Watcher()
	}
	return &Waiter{
		watcher: watcher,
		kinds:   make(map[string]map[string]bool),
	}
}

func (w *Waiter) Add(resource string) error {
	cresource := w.watcher.Canonical(resource)

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
		err = w.Add(fmt.Sprintf("%s/%s", res.Kind(), res.QName()))
		if err != nil {
			return
		}
	}
	return
}

func (w *Waiter) ScanPaths(files []string) (err error) {
	resources, err := WalkResources(tpu.IsYaml, files...)
	for _, res := range resources {
		err = w.Add(fmt.Sprintf("%s/%s", res.Kind(), res.QName()))
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
			lastIf, ok := r["lastTimestamp"]
			if ok {
				last, err := time.Parse("2006-01-02T15:04:05Z", lastIf.(string))
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
				objIf, ok := r["involvedObject"]
				if ok {
					obj, ok := objIf.(map[string]interface{})
					if ok {
						name = fmt.Sprintf("%s/%v.%v", obj["kind"], obj["name"],
							obj["namespace"])
						name = watcher.Canonical(name)
					} else {
						name = r.QName()
					}
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
						fmt.Printf("ready: %s/%s\n", watcher.Canonical(r.Kind()), r.QName())
					} else {
						fmt.Printf("ready: %s/%s (UNIMPLEMENTED)\n",
							watcher.Canonical(r.Kind()), r.QName())
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
			log.Fatal(err)
		}
	}

	go func() {
		time.Sleep(time.Duration(timeout) * time.Second)
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
