package main

import (
	"fmt"
	"github.com/datawire/teleproxy/pkg/k8s"
)

func main() {
	var err error
	fmt.Println("Starting...")

	w := k8s.NewClient(nil).Watcher()
	//err = w.Watch("namespaces", func(watcher *k8s.Watcher) {
	//	fmt.Println("namespaces changed!")
	//})

	err = w.WatchNamespace("", "endpoints", func(watcher *k8s.Watcher) {
		fmt.Println("stuff changed!")
	})

	w.Start()

	services := w.List("endpoints")
	for _, s := range services {
		fmt.Println(s.Name())
	}

	if err != nil {
		panic(err)
	}

	//ticker := time.NewTicker(5 * time.Second).C
	for {
		select {
		//case <- ticker:
		//	kinds := []string{"namespaces"}
		//	for _, k := range kinds {
		//		resources := w.List(k)
		//		rBytes, _ := k8s.MarshalResourcesJSON(resources)
		//		fmt.Println(string(rBytes))
		//	}
		}
	}
}
