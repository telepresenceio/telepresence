package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/datawire/teleproxy/pkg/dtest"
	"github.com/datawire/teleproxy/pkg/dtest/testprocess"
)

const ClusterFile = "../../build-aux/cluster.knaut"

func TestMain(m *testing.M) {
	testprocess.Dispatch()
	dtest.WithMachineLock(func() {
		dtest.K8sApply(ClusterFile, "../../k8s")
		os.Exit(m.Run())
	})
}

func withInterrupt(t *testing.T, cmd *exec.Cmd, body func()) {
	err := cmd.Start()
	if err != nil {
		t.Fatal(err)
		return
	}

	exited := make(chan bool)
	go func() {
		err := cmd.Wait()
		if err != nil {
			t.Error(err)
		}
		close(exited)
	}()

	defer func() {
		err := cmd.Process.Signal(os.Interrupt)
		if err != nil {
			t.Error(err)
		}
		<-exited
	}()

	body()
}

// use this get to avoid artifacts from idle connections
func get(url string) (*http.Response, error) {
	http.DefaultClient.CloseIdleConnections()
	/* #nosec */
	return http.Get(url)
}

// The poll function polls the supplied url until we get back a 200 or
// time out.
func poll(t *testing.T, url string) bool {
	start := time.Now()
	for {
		resp, err := get(url)
		if err != nil {
			log.Print(err)
		} else if resp.StatusCode == 200 {
			log.Printf("%s: SUCCESS", url)
			return true
		}
		if t.Failed() {
			t.Errorf("giving up because we have already failed")
			return false
		}
		time.Sleep(time.Second)
		if time.Since(start) > 30*time.Second {
			t.Errorf("time has expired")
			return false
		}
	}
}

func teleproxyCluster() {
	os.Args = []string{"teleproxy", fmt.Sprintf("-kubeconfig=%s", ClusterFile)}
	main()
}

var smoke = testprocess.MakeSudo(teleproxyCluster)

func TestSmoke(t *testing.T) {
	withInterrupt(t, smoke, func() {
		poll(t, "http://teleproxied-httpbin/status/200")
	})
}
