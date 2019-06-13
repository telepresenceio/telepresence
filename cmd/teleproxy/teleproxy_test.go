package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/datawire/teleproxy/pkg/dtest"
)

const CLUSTER_FILE = "../../build-aux/cluster.knaut"

func TestMain(m *testing.M) {
	dtest.Subprocess.Enable()
	dtest.WithGlobalLock(func() {
		dtest.Manifests(CLUSTER_FILE, "../../k8s")
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
		if strings.HasSuffix(cmd.Path, "/sudo") {
			// not totally sure why the -b is necessary
			// but I think it might be because sudo tries
			// to be smart about only relaying signals
			// some of the time
			args := []string{"-b", "kill", "-INT", fmt.Sprintf("%d", cmd.Process.Pid)}
			log.Printf("sudo %s", strings.Join(args, " "))
			kill := exec.Command("sudo", args...)
			kill.Stdout = os.Stdout
			kill.Stderr = os.Stderr
			err := kill.Run()
			if err != nil {
				log.Print(err)
				t.Error(err)
			}
		} else {
			cmd.Process.Signal(os.Interrupt)
		}
		<-exited
	}()

	body()
}

// use this get to avoid artifacts from idle connections
func get(url string) (*http.Response, error) {
	http.DefaultClient.CloseIdleConnections()
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

func teleproxy_cluster() {
	os.Args = []string{"teleproxy", fmt.Sprintf("-kubeconfig=%s", CLUSTER_FILE)}
	main()
}

var smoke = dtest.Subprocess.MakeSudo(teleproxy_cluster)

func TestSmoke(t *testing.T) {
	withInterrupt(t, smoke, func() {
		poll(t, "http://teleproxied-httpbin/status/200")
	})
}
