package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datawire/teleproxy/internal/pkg/k8s/watcher"
	"gopkg.in/yaml.v2"
)

func isYaml(name string) bool {
	for _, ext := range []string{
		".yaml",
	} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

type ResourceSet struct {
	w     *watcher.Watcher
	kinds map[string]map[string]bool
}

func (rs *ResourceSet) add(resource string) error {
	parts := strings.Split(resource, "/")
	if len(parts) != 2 {
		return fmt.Errorf("expecting <kind>/<name>, got %s", resource)
	}

	kind := rs.w.Canonical(parts[0])
	name := parts[1]

	resources, ok := rs.kinds[kind]
	if !ok {
		resources = make(map[string]bool)
		rs.kinds[kind] = resources
	}
	resources[name] = false
	return nil
}

func (rs *ResourceSet) scan(path string) (err error) {
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
		res := watcher.NewResourceFromYaml(uns)
		err = rs.add(fmt.Sprintf("%s/%s", res.Kind(), res.Name()))
		if err != nil {
			return
		}
	}
}

func (rs *ResourceSet) remove(kind, name string) {
	delete(rs.kinds[kind], name)
}

func (rs *ResourceSet) isEmpty() bool {
	for _, names := range rs.kinds {
		if len(names) > 0 {
			return false
		}
	}

	return true
}

var timeout = flag.Int("t", 60, "timeout in seconds")
var file = flag.String("f", "", "path to yaml file")

func main() {
	flag.Parse()

	w := watcher.NewWatcher(os.Getenv("KUBECONFIG"))
	rset := ResourceSet{w, make(map[string]map[string]bool)}

	if *file != "" {
		err := filepath.Walk(*file, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if !info.IsDir() && isYaml(path) {
				err := rset.scan(path)
				if err != nil {
					log.Fatal(err)
				}
			}

			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
	}

	for _, resource := range flag.Args() {
		err := rset.add(resource)
		if err != nil {
			log.Fatal(err)
		}
	}

	listener := func(w *watcher.Watcher) {
		for kind, names := range rset.kinds {
			for name := range names {
				r := w.Get(kind, name)
				if r.Ready() {
					fmt.Printf("ready: %s/%s\n", w.Canonical(r.Kind()), r.Name())
					rset.remove(kind, name)
				}
			}
		}

		if rset.isEmpty() {
			w.Stop()
		}
	}

	for k := range rset.kinds {
		err := w.Watch(k, listener)
		if err != nil {
			log.Fatal(err)
		}
	}

	go func() {
		time.Sleep(time.Duration(*timeout) * time.Second)
		w.Stop()
	}()

	w.Wait()

	code := 0

	for kind, names := range rset.kinds {
		for name := range names {
			fmt.Printf("not ready: %s/%s\n", kind, name)
			code = 1
		}
	}

	os.Exit(code)
}
