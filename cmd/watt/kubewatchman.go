package main

import (
	"fmt"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type k8sEvent struct {
	watchId   string
	kind      string
	resources []k8s.Resource
}

type KubernetesWatchMaker struct {
	kubeAPI *k8s.Client
	notify  chan<- k8sEvent
}

func (m *KubernetesWatchMaker) MakeKubernetesWatch(spec KubernetesWatchSpec) (*supervisor.Worker, error) {
	var worker *supervisor.Worker
	var err error

	worker = &supervisor.Worker{
		Name: fmt.Sprintf("kubernetes:%s", spec.WatchId()),
		Work: func(p *supervisor.Process) error {
			watcher := m.kubeAPI.Watcher()
			watchFunc := func(watchId, ns, kind string) func(watcher *k8s.Watcher) {
				return func(watcher *k8s.Watcher) {
					resources := watcher.List(kind)
					p.Logf("found %d %q in namespace %q", len(resources), kind, fmtNamespace(ns))
					m.notify <- k8sEvent{watchId: watchId, kind: kind, resources: resources}
					p.Logf("sent %q to receivers", kind)
				}
			}

			watcherErr := watcher.SelectiveWatch(spec.Namespace, spec.Kind, spec.FieldSelector, spec.LabelSelector,
				watchFunc(spec.WatchId(), spec.Namespace, spec.Kind))

			if watcherErr != nil {
				return watcherErr
			}

			watcher.Start()
			<-p.Shutdown()
			watcher.Stop()
			return nil
		},

		Retry: true,
	}

	return worker, err
}

type kubewatchman struct {
	WatchMaker IKubernetesWatchMaker
	watched    map[string]*supervisor.Worker
	in         <-chan []KubernetesWatchSpec
}

func (w *kubewatchman) Work(p *supervisor.Process) error {
	p.Ready()

	w.watched = make(map[string]*supervisor.Worker)

	for {
		select {
		case watches := <-w.in:
			found := make(map[string]*supervisor.Worker)
			p.Logf("processing %d kubernetes watch specs", len(watches))
			for _, spec := range watches {
				worker, err := w.WatchMaker.MakeKubernetesWatch(spec)
				if err != nil {
					p.Logf("failed to create kubernetes watcher: %v", err)
					continue
				}

				if _, exists := w.watched[worker.Name]; exists {
					found[worker.Name] = w.watched[worker.Name]
				} else {
					p.Logf("add kubernetes watcher %s\n", worker.Name)
					p.Supervisor().Supervise(worker)
					w.watched[worker.Name] = worker
					found[worker.Name] = worker
				}
			}

			for workerName, worker := range w.watched {
				if _, exists := found[workerName]; !exists {
					p.Logf("remove kubernetes watcher %s\n", workerName)
					worker.Shutdown()
					worker.Wait()
				}
			}

			w.watched = found
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			return nil
		}
	}
}

type kubebootstrap struct {
	namespace      string
	kinds          []string
	fieldSelector  string
	labelSelector  string
	notify         []chan<- k8sEvent
	kubeAPIWatcher *k8s.Watcher
}

func fmtNamespace(ns string) string {
	if ns == "" {
		return "*"
	}

	return ns
}

func (b *kubebootstrap) Work(p *supervisor.Process) error {
	for _, kind := range b.kinds {
		p.Logf("adding kubernetes watch for %q in namespace %q", kind, fmtNamespace(kubernetesNamespace))

		watcherFunc := func(ns, kind string) func(watcher *k8s.Watcher) {
			return func(watcher *k8s.Watcher) {
				resources := watcher.List(kind)
				p.Logf("found %d %q in namespace %q", len(resources), kind, fmtNamespace(ns))
				for _, n := range b.notify {
					n <- k8sEvent{kind: kind, resources: resources}
				}
				p.Logf("sent %q to %d receivers", kind, len(b.notify))
			}
		}

		err := b.kubeAPIWatcher.SelectiveWatch(b.namespace, kind, b.fieldSelector, b.labelSelector, watcherFunc(b.namespace, kind))

		if err != nil {
			return err
		}
	}

	b.kubeAPIWatcher.Start()
	p.Ready()

	for range p.Shutdown() {
		p.Logf("shutdown initiated")
		b.kubeAPIWatcher.Stop()
	}

	return nil
}
