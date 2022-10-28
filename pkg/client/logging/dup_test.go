package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

func head(str string, n int) string {
	end := 0
	for i := 0; i < n; i++ {
		nl := strings.IndexByte(str[end:], '\n')
		if nl < 0 {
			return str
		}
		end += nl + 1
	}
	return str[:end]
}

func TestDupToStd(t *testing.T) {
	dirname := t.TempDir()

	ctx := dlog.NewTestContext(t, true)
	cmd := dexec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run="+t.Name()+"Helper", "--", dirname)
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1")

	err := cmd.Run()
	var eerr *dexec.ExitError
	require.ErrorAs(t, err, &eerr)
	require.True(t, eerr.Exited())
	require.Equal(t, 2, eerr.ExitCode())

	content, err := os.ReadFile(filepath.Join(dirname, "log.txt"))
	require.NoError(t, err)
	require.Equal(t, "this is stdout\nthis is stderr\npanic: this is panic\n",
		head(string(content), 3))
}

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Exit(testDupToStdHelper())
	}
	os.Exit(m.Run())
}

func testDupToStdHelper() int {
	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "expected exactly 1 argument, got %d\n", len(args))
		return 1
	}

	dirname := args[0]

	file, err := os.OpenFile(filepath.Join(dirname, "log.txt"), os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		return 1
	}

	if err := dupToStdOut(file); err != nil {
		fmt.Fprintf(os.Stderr, "dup: %v\n", err)
		return 1
	}

	if err := dupToStdErr(file); err != nil {
		fmt.Fprintf(os.Stderr, "dup: %v\n", err)
		return 1
	}

	fmt.Println("this is stdout")
	fmt.Fprintln(os.Stderr, "this is stderr")
	panic("this is panic")
}
