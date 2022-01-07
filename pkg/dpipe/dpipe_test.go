package dpipe

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"

	"github.com/datawire/dlib/dlog"
)

var echoBinary string

func TestMain(m *testing.M) {
	ebf, err := os.CreateTemp("", "echo")
	if err != nil {
		panic(err)
	}
	echoBinary = ebf.Name()
	if runtime.GOOS == "windows" {
		echoBinary += ".exe"
	}
	ebf.Close()
	if err = exec.Command("go", "build", "-o", echoBinary, "./testdata/echo").Run(); err != nil {
		panic(err)
	}
	defer os.Remove(echoBinary)
	m.Run()
}

type bufClose struct {
	bytes.Buffer
}

func (b *bufClose) Close() error {
	return nil
}

func makeLoggerOn(bf *bytes.Buffer) context.Context {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)
	logger.SetOutput(bf)
	return dlog.WithLogger(context.Background(), dlog.WrapLogrus(logger))
}

// Test that stdout of a process executed by DPipe is sent to peer
func TestDPipe_stdout(t *testing.T) {
	log := &bytes.Buffer{}
	ctx := makeLoggerOn(log)
	peer := &bufClose{}
	assert.NoError(t, DPipe(ctx, peer, echoBinary, "-d", "1", "hello stdout"))
	assert.Empty(t, log)
	assert.Equal(t, peer.String(), "hello stdout\n")
}

// Test that stderr of a process executed by DPipe is logged as errors
func TestDPipe_stderr(t *testing.T) {
	log := &bytes.Buffer{}
	ctx := makeLoggerOn(log)
	peer := &bufClose{}
	assert.NoError(t, DPipe(ctx, peer, echoBinary, "-d", "2", "hello stderr"))
	assert.Contains(t, log.String(), `level=error msg="hello stderr"`)
	assert.Empty(t, peer)
}
