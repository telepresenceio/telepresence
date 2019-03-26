package main

import (
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type k8sEvent struct {
	kind      string
	resources []k8s.Resource
}

type kubewatchman struct {
	namespace string
	kinds     []string
	notify    []chan<- k8sEvent
}

func fmtNamespace(ns string) string {
	if ns == "" {
		return "*"
	}

	return ns
}

func (w *kubewatchman) Work(p *supervisor.Process) error {
	kubeAPIWatcher := k8s.NewClient(nil).Watcher()

	for _, kind := range w.kinds {
		p.Logf("adding kubernetes watch for %q in namespace %q", kind, fmtNamespace(kubernetesNamespace))

		watcherFunc := func(ns, kind string) func(watcher *k8s.Watcher) {
			return func(watcher *k8s.Watcher) {
				resources := watcher.List(kind)
				p.Logf("found %d %q in namespace %q", len(resources), kind, fmtNamespace(ns))
				for _, n := range w.notify {
					n <- k8sEvent{kind: kind, resources: resources}
				}
				p.Logf("sent %q to %d receivers", kind, len(w.notify))
			}
		}

		err := kubeAPIWatcher.WatchNamespace(w.namespace, kind, watcherFunc(w.namespace, kind))

		if err != nil {
			return err
		}
	}

	kubeAPIWatcher.Start()
	p.Ready()

	for range p.Shutdown() {
		p.Logf("shutdown initiated")
		kubeAPIWatcher.Stop()
	}

	return nil

	// gosimple complains this is unnecessary compared to above
	//for {
	//	select {
	//	case <-p.Shutdown():
	//		p.Logf("shutdown initiated")
	//		kubeAPIWatcher.Stop()
	//		return nil
	//	}
	//}
}
