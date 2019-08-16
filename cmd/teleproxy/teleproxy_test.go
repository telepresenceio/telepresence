package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/datawire/teleproxy/pkg/dtest"
	"github.com/datawire/teleproxy/pkg/dtest/testprocess"
)

var noDocker error

func TestMain(m *testing.M) {
	dtest.Sudo()
	testprocess.Dispatch()
	dtest.WithMachineLock(func() {
		_, noDocker = exec.LookPath("docker")
		if noDocker == nil {
			dtest.K8sApply("../../k8s")
		}
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

var netClient = &http.Client{
	Timeout: time.Second * 10,
}

// use this get to avoid artifacts from idle connections
func get(url string) (*http.Response, error) {
	netClient.CloseIdleConnections()
	/* #nosec */
	return netClient.Get(url)
}

// The poll function polls the supplied url until we get back a 200 or
// time out.
// nolint
func poll(t *testing.T, url string, expected string) bool {
	start := time.Now()
	for {
		b := func() bool {
			resp, err := get(url)
			if err != nil {
				log.Printf("poll get: %v", err)
				return false
			}
			defer resp.Body.Close()

			bytes, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				log.Printf("poll read body: %v", err)
				return false
			}

			body := string(bytes)

			if resp.StatusCode == 200 && (body == expected || expected == "") {
				log.Printf("%s: SUCCESS", url)
				return true
			}

			log.Printf("GOT %d: %q, expected %d: %q", resp.StatusCode, body, 200, expected)
			return false
		}()
		if b {
			return true
		}

		if t.Failed() {
			t.Errorf("giving up because we have already failed")
			return false
		}
		time.Sleep(time.Second)
		if time.Since(start) > 120*time.Second {
			t.Errorf("time has expired")
			return false
		}
	}
}

func teleproxyCluster() {
	os.Args = []string{"teleproxy", fmt.Sprintf("--kubeconfig=%s", dtest.Kubeconfig())}
	main()
}

var smoke = testprocess.MakeSudo(teleproxyCluster)

func TestSmoke(t *testing.T) {
	if noDocker != nil {
		t.Skip(noDocker)
	}
	withInterrupt(t, smoke, func() {
		poll(t, "http://httptarget", "")
	})
}

var orig = testprocess.MakeSudo(teleproxyCluster)
var dup = testprocess.MakeSudo(teleproxyCluster)

func TestAlreadyRunning(t *testing.T) {
	if noDocker != nil {
		t.Skip(noDocker)
	}
	withInterrupt(t, orig, func() {
		if poll(t, "http://httptarget", "") {
			err := dup.Run()
			t.Logf("ERROR: %v", err)
			resp, err := get("http://httptarget")
			if err != nil {
				t.Errorf("duplicate teleproxy killed the first one: %v", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Errorf("duplicate teleproxy killed the first one: %v", resp.StatusCode)
			}
		}
	})
}

const HupConfig = "/tmp/teleproxy_test_hup_cluster.yaml"

var hup = testprocess.MakeSudo(func() {
	os.Args = []string{"teleproxy", fmt.Sprintf("--kubeconfig=%s", HupConfig)}
	main()
})

func writeGoodFile(dest string) {
	good, err := ioutil.ReadFile(dtest.Kubeconfig())
	if err != nil {
		panic(err)
	}

	err = ioutil.WriteFile(dest, good, 0666)
	if err != nil {
		panic(err)
	}
}

// nolint
func writeBadFile(dest string) {
	err := ioutil.WriteFile(dest, []byte("BAD FILE"), 0666)
	if err != nil {
		panic(err)
	}
}

func writeAltFile(dest string) {
	good, err := ioutil.ReadFile(dtest.Kubeconfig())
	if err != nil {
		panic(err)
	}

	var obj map[string]interface{}
	err = yaml.Unmarshal(good, &obj)
	if err != nil {
		panic(err)
	}

	current := obj["current-context"].(string)
	ictxs := obj["contexts"].([]interface{})
	for _, ictx := range ictxs {
		ctx := ictx.(map[string]interface{})
		if ctx["name"] == current {
			ctx["context"].(map[string]interface{})["namespace"] = "alt"
		}
	}

	alt, err := yaml.Marshal(obj)
	if err != nil {
		panic(err)
	}

	err = ioutil.WriteFile(dest, alt, 0666)
	if err != nil {
		panic(err)
	}
}

// We should really do something sane in each case:
//  - startup with good, switch to bad
//  - startup with bad, switch to good
//  - startup with good, switch to alt
// Currently we only cover good to alternative good, I haven't figured
// out what makes sense in the other cases

func TestHUPGood2Alt(t *testing.T) {
	if noDocker != nil {
		t.Skip(noDocker)
	}
	gotHere := false
	writeGoodFile(HupConfig)
	withInterrupt(t, hup, func() {
		if poll(t, "http://httptarget", "HTTPTEST") {
			writeAltFile(HupConfig)
			err := hup.Process.Signal(syscall.SIGHUP)
			if err != nil {
				t.Errorf("error sending signal: %v", err)
				return
			}
			if poll(t, "http://httptarget", "ALT") {
				gotHere = true
				return
			}
		}
	})
	if !gotHere {
		t.Errorf("didn't get there")
	}
}
