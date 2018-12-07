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
	k := NewKeeper("TST", "echo hi >> /tmp/lines")
	k.Start()
	time.Sleep(3500 * time.Millisecond)
	k.Stop()
	k.Wait()
	dat, err := ioutil.ReadFile("/tmp/lines")
	if err != nil {
		panic(err)
	}
	lines := bytes.Count(dat, []byte("\n"))
	if lines != 4 {
		t.Errorf("incorrect number of lines: %v", 4)
	}
}
