package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"time"
)

func main() {
	url := "http://localhost:9876"
	client := http.Client{Timeout: 5 * time.Second}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		err := func() error {
			res, err := client.Get(url)
			if err != nil {
				return err
			}
			body, err := ioutil.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				return err
			}
			for len(body) > 0 {
				n, err := w.Write(body)
				body = body[n:]
				if err != nil {
					return err
				}
			}
			return nil
		}()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	})

	log.Fatal(http.ListenAndServe("localhost:8910", nil))
}
