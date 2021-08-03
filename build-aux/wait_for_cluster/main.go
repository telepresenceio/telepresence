//+build ignore

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("Usage: %s <kubeconfig>", os.Args[0])
	}
	kubeconfig := os.Args[1]
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("Unable to build config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("Unable to build clientset: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	return client.Retry(ctx, "connect", func(ctx context.Context) error {
		fmt.Println("Trying to connect to cluster...")
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, err := clientset.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
		return err
	}, 2*time.Second, 30*time.Second)
}

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Cluster is ready")
}
