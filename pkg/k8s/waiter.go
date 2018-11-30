package k8s

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datawire/teleproxy/internal/pkg/tpu"

	"gopkg.in/yaml.v2"
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
	file, err := os.Open(path)
	if err != nil {
		return
	}
	d := yaml.NewDecoder(file)
	for {
		var uns map[interface{}]interface{}
		err = d.Decode(&uns)
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return
		}
		res := NewResourceFromYaml(uns)
		err = w.Add(fmt.Sprintf("%s/%s", res.Kind(), res.QName()))
		if err != nil {
			return
		}
	}
}

func (w *Waiter) ScanPaths(files []string) error {
	for _, file := range files {
		err := filepath.Walk(file, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if !info.IsDir() && tpu.IsYaml(path) {
				err := w.Scan(path)
				if err != nil {
					return err
				}
			}

			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
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
	listener := func(watcher *Watcher) {
		for kind, names := range w.kinds {
			for name := range names {
				r := watcher.Get(kind, name)
				if r.Ready() {
					fmt.Printf("ready: %s/%s\n", watcher.Canonical(r.Kind()), r.QName())
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
