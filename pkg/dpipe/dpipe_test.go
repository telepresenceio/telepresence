package dpipe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec" //nolint:depguard // This short script has no logging and no Contexts.
	"runtime"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"

	"github.com/datawire/dlib/dlog"
)

var echoBinary string

func TestMain(m *testing.M) {
	ebf, err := os.CreateTemp("", "echo")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	echoBinary = ebf.Name()
	if runtime.GOOS == "windows" {
		echoBinary += ".exe"
	}
	ebf.Close()
	if err = exec.Command("go", "build", "-o", echoBinary, "./testdata/echo").Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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

// Test that stdout of a process executed by DPipe is sent to peer.
func TestDPipe_stdout(t *testing.T) {
	log := &bytes.Buffer{}
	ctx := makeLoggerOn(log)
	peer := &bufClose{}
	assert.NoError(t, DPipe(ctx, peer, echoBinary, "-d", "1", "hello stdout"))
	assert.Empty(t, log)
	assert.Equal(t, "hello stdout\n", peer.String())
}

// Test that stderr of a process executed by DPipe is logged as errors.
func TestDPipe_stderr(t *testing.T) {
	log := &bytes.Buffer{}
	ctx := makeLoggerOn(log)
	peer := &bufClose{}
	assert.NoError(t, DPipe(ctx, peer, echoBinary, "-d", "2", "hello stderr"))
	time.Sleep(time.Second)
	assert.Contains(t, log.String(), `level=error msg="hello stderr"`)
	assert.Empty(t, peer.String())
}
