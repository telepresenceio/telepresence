//+build ignore

package main

import (
	"context"
	"fmt"
	"os"
	"time"

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
	ready := false
	for ctx.Err() == nil && !ready {
		ready, err = func() (bool, error) {
			fmt.Println("Trying to connect to cluster...")
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_, err := clientset.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
			if ctx.Err() == nil && err != nil {
				return false, err
			}
			return ctx.Err() == nil, nil
		}()
		if err != nil {
			return fmt.Errorf("Error from API request: %w", err)
		}
		time.Sleep(1 * time.Second)
	}
	return nil
}

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Cluster is ready")
}
