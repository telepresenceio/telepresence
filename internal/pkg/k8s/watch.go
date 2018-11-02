package k8s

import (
	"log"
	"time"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/cache"
)

func Watch(kubeconfig string, reconcile func([]*v1.Service)) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	
	watchlist := cache.NewListWatchFromClient(clientset.Core().RESTClient(), "services", v1.NamespaceAll,
		fields.Everything())

	var store cache.Store

	var notify = func () {
		objs := store.List()
		svcs := make([]*v1.Service, len(objs))
		for idx, obj := range objs {
			svcs[idx] = obj.(*v1.Service)
		}
		reconcile(svcs)
	}

	store, controller := cache.NewInformer(
		watchlist,
		&v1.Service{},
		time.Second * 0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				log.Printf("ADDED: %s->%s\n", svc.Name, svc.Spec.ClusterIP)
				notify()
			},
			DeleteFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				log.Printf("DELETED: %s\n", svc.Name)
				notify()
			},
			UpdateFunc:func(oldObj, newObj interface{}) {
				svc := newObj.(*v1.Service)
				log.Printf("CHANGED: %s->%s\n", svc.Name, svc.Spec.ClusterIP)
				notify()
			},
		},
	)
	stop := make(chan struct{})
	go controller.Run(stop)
}
