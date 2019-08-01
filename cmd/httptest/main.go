package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	body := os.Getenv("HTTPTEST_BODY")
	if body == "" {
		body = "HTTPTEST"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(body))
		if err != nil {
			log.Print(err)
		}
	})

	log.Fatal(http.ListenAndServe(":8080", nil))
}
