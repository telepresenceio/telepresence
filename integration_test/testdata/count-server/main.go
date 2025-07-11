package main

import (
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
)

func main() {
	counter := int64(0)
	http.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, atomic.LoadInt64(&counter)) })

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&counter, 1)
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
