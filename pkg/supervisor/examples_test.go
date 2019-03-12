package supervisor

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
)

func Example() {
	// We are going to create a server and a client worker. The
	// server needs to first bind to a port before it is ready to
	// use. By listing the dependency between the client and the
	// server worker, the client will not be started until the
	// server signals that it is ready.
	ctx := context.Background()
	s := WithContext(ctx)
	var addr string
	s.Supervise(&Worker{
		Name: "server",
		Work: func(p *Process) error {
			// :0 will ask for an unused port
			l, err := net.Listen("tcp", ":0")
			if err != nil {
				return err
			}
			defer l.Close()
			// store the unused port that was allocated so
			// that the client knows where to talk to
			addr = l.Addr().String()
			p.Logf("listening on %s", addr)

			http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("hello"))
				s.Shutdown()
			})

			srv := &http.Server{}
			// launch an anonymous child worker to serve requests
			p.Go(func(p *Process) error {
				return srv.Serve(l)
			})

			fmt.Println("server ready")
			// signal that we are ready
			p.Ready()

			<-p.Shutdown() // await graceful shutdown signal
			return srv.Shutdown(p.Context())
		},
	})
	s.Supervise(&Worker{
		Name:     "client",
		Requires: []string{"server"},
		Work: func(p *Process) error {
			resp, err := http.Get(fmt.Sprintf("http://%s", addr))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			bytes, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			fmt.Printf("client %s\n", string(bytes))
			return nil
		},
	})
	errors := s.Run()
	for _, err := range errors {
		log.Println(err.Error())
	}
	// Output: server ready
	// client hello
}

func ExampleSupervisor() {
	ctx := context.Background()
	s := WithContext(ctx)
	for idx, url := range []string{"https://www.google.com", "https://www.bing.com"} {
		url_capture := url
		s.Supervise(&Worker{
			Name: fmt.Sprintf("url-%d", idx),
			Work: func(p *Process) error {
				resp, err := http.Get(url)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				fmt.Printf("url %s: %s\n", url_capture, resp.Status)
				return nil
			},
		})
	}

	// The Run() method will block until all workers are done. Any
	// and all background errors in workers, including panics are
	// returned by Run().
	errors := s.Run()
	for _, err := range errors {
		fmt.Println(err)
	}
	// Unordered output: url https://www.bing.com: 200 OK
	// url https://www.google.com: 200 OK
}
