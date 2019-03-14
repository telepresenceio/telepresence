package main

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	kube *kubernetes.Clientset
}

func newKubeClient() (*kubernetes.Clientset, error) {
	config, err := clientcmd.BuildConfigFromFlags("", "/home/plombardi/.kube/phil.yaml")
	if err != nil {
		panic(err.Error())
	}

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	return clientSet, err
}

func main() {
	config, err := clientcmd.BuildConfigFromFlags("", "/home/plombardi/.kube/phil.yaml")
	if err != nil {
		panic(err.Error())
	}

	//clientSet, err := kubernetes.NewForConfig(config)
	//if err != nil {
	//	panic(err.Error())
	//}

	kube, err := dynamic.NewForConfig(config)
	kube.Resource(schema.GroupVersionResource{}).Watch()
}
