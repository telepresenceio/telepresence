package tpu

import (
	"bytes"
	"io/ioutil"
	"os"
	"testing"
	"time"
)

func TestKeepalive(t *testing.T) {
	os.Remove("/tmp/lines")
	k := Keepalive(0, "", "sh", "-c", "echo hi >> /tmp/lines")
	time.Sleep(3500 * time.Millisecond)
	k.Shutdown()
	dat, err := ioutil.ReadFile("/tmp/lines")
	if err != nil {
		panic(err)
	}
	lines := bytes.Count(dat, []byte("\n"))
	if lines != 4 {
		t.Errorf("incorrect number of lines: %v", 4)
	}
}
