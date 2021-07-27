package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/datawire/dlib/dhttp"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := "9000"
	sc := &dhttp.ServerConfig{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "hello from intercept at %s", r.URL.Path)
		}),
	}
	err := sc.ListenAndServe(ctx, ":"+port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v", err)
		os.Exit(1)
	}
}
