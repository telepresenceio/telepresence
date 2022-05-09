package output

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestWithOutput(t *testing.T) {
	expectedREStdout := "re\n"
	expectedREStderr := "re_stderr\n"
	expectedName := "testing"

	re := func(cmd *cobra.Command, args []string) error {
		stdout := cmd.OutOrStdout()
		stderr := cmd.ErrOrStderr()
		fmt.Fprint(stdout, expectedREStdout)
		fmt.Fprint(stderr, expectedREStderr)
		return nil
	}

	newCmdWithBufs := func() (*cobra.Command, *strings.Builder, *strings.Builder) {
		stdoutBuf := strings.Builder{}
		stderrBuf := strings.Builder{}
		cmd := cobra.Command{}

		cmd.Use = expectedName
		cmd.SetOut(&stdoutBuf)
		cmd.SetErr(&stderrBuf)
		cmd.RunE = re

		cmd.Flags().String("output", "default", "")

		return &cmd, &stdoutBuf, &stderrBuf
	}

	t.Run("non-json output", func(t *testing.T) {
		cmd, outBuf, errBuf := newCmdWithBufs()
		ctx := WithStructure(context.Background(), cmd)

		err := cmd.ExecuteContext(ctx)
		if err != nil {
			t.Errorf("expected nil err, instead got: %s", err.Error())
		}

		stdout := outBuf.String()
		expectedStdout := expectedREStdout
		if stdout != expectedStdout {
			t.Errorf("did not get expected stdout, got: %s", stdout)
		}

		stderr := errBuf.String()
		expectedStderr := expectedREStderr
		if stderr != expectedStderr {
			t.Errorf("did not get expected stderr, got: %s", stderr)
		}
	})

	t.Run("json output no error", func(t *testing.T) {
		cmd, outBuf, errBuf := newCmdWithBufs()
		ctx := WithStructure(context.Background(), cmd)

		cmd.SetArgs([]string{"--output=json"})

		err := cmd.ExecuteContext(ctx)
		if err != nil {
			t.Errorf("expected nil err, instead got: %s", err.Error())
		}

		stdout := outBuf.String()
		m := map[string]string{}
		if err := json.Unmarshal([]byte(stdout), &m); err != nil {
			t.Errorf("did not get json as stdout, got: %s", stdout)
		}

		if m["stdout"] != expectedREStdout {
			t.Errorf("did not get expected stdout, got: %s", m["stdout"])
		}

		if m["stderr"] != expectedREStderr {
			t.Errorf("did not get expected stderr, got: %s", m["stderr"])
		}

		if m["cmd"] != expectedName {
			t.Errorf("did not get expected cmd name, got: %s", m["cmd"])
		}

		stderr := errBuf.String()
		if stderr != "" {
			t.Errorf("expected empty stderr, got: %s", stderr)
		}
	})

	t.Run("json output with error", func(t *testing.T) {
		expectedErr := "ERROR"
		cmd, outBuf, _ := newCmdWithBufs()
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			return errors.New(expectedErr)
		}
		ctx := WithStructure(context.Background(), cmd)

		cmd.SetArgs([]string{"--output=json"})

		err := cmd.ExecuteContext(ctx)
		if err != nil {
			t.Errorf("expected nil err, instead got: %s", err.Error())
		}

		stdout := outBuf.String()
		m := map[string]string{}
		if err := json.Unmarshal([]byte(stdout), &m); err != nil {
			t.Errorf("did not get json as stdout, got: %s", stdout)
		}

		if m["err"] != expectedErr {
			t.Errorf("did not get expected err, got: %s", m["err"])
		}
	})

	t.Run("json output with native json", func(t *testing.T) {
		expectedNativeJSONMap := map[string]float64{
			"a": 1,
		}
		cmd, outBuf, errBuf := newCmdWithBufs()
		cmd.LocalFlags().Bool("json", false, "")
		_ = cmd.LocalFlags().Set("json", "true")
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			stdout := cmd.OutOrStdout()
			_ = json.NewEncoder(stdout).Encode(expectedNativeJSONMap)
			return nil
		}
		ctx := WithStructure(context.Background(), cmd)

		cmd.SetArgs([]string{"--output=json"})

		err := cmd.ExecuteContext(ctx)
		if err != nil {
			t.Errorf("expected nil err, instead got: %s", err.Error())
		}

		stdout := outBuf.String()
		m := map[string]interface{}{}
		if err := json.Unmarshal([]byte(stdout), &m); err != nil {
			t.Errorf("did not get json as stdout, got: %s", stdout)
		}

		jsonOutputBytes, err := json.Marshal(m["stdout"])
		if err != nil {
			t.Errorf("did not get json stdout as expected")
		}
		expectedJSONOutputBytes, _ := json.Marshal(expectedNativeJSONMap)

		if string(jsonOutputBytes) != string(expectedJSONOutputBytes) {
			t.Errorf("did not get expected stdout json, got: %+v", m["stdout"])
		}

		stderr := errBuf.String()
		if stderr != "" {
			t.Errorf("expected empty stderr, got: %s", stderr)
		}
	})

	t.Run("PersistentPreRun", func(t *testing.T) {
		cmd, _, _ := newCmdWithBufs()
		pprRan := false
		cmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
			pprRan = true
		}
		ctx := WithStructure(context.Background(), cmd)

		err := cmd.ExecuteContext(ctx)
		if err != nil {
			t.Errorf("expected nil err, instead got: %s", err.Error())
		}

		if !pprRan {
			t.Error("PersistentPreRun did not run")
		}
	})

	t.Run("no RunE", func(t *testing.T) {
		cmd, outBuf, errBuf := newCmdWithBufs()
		cmd.RunE = nil
		cmd.Run = func(cmd *cobra.Command, args []string) {
			_ = re(cmd, args)
		}
		ctx := WithStructure(context.Background(), cmd)

		cmd.SetArgs([]string{"--output=json"})

		err := cmd.ExecuteContext(ctx)
		if err != nil {
			t.Errorf("expected nil err, instead got: %s", err.Error())
		}

		stdout := outBuf.String()
		if stdout != expectedREStdout {
			t.Errorf("did not get expected stdout, got: %s", stdout)
		}

		stderr := errBuf.String()
		if stderr != expectedREStderr {
			t.Errorf("did not get expected stderr, got: %s", stderr)
		}
	})
}

func TestOutputs(t *testing.T) {
	t.Run("no output in context", func(t *testing.T) {
		ctx := context.Background()
		stdout, stderr := Structured(ctx)

		if stdout != os.Stdout {
			t.Errorf("expected stdout to be os.Stdout")
		}

		if stderr != os.Stderr {
			t.Errorf("expected stdout to be os.Stderr")
		}
	})

	t.Run("with output in context", func(t *testing.T) {
		ctx := context.Background()
		o := output{
			stdoutBuf: strings.Builder{},
			stderrBuf: strings.Builder{},
		}

		ctx = context.WithValue(ctx, key{}, &o)
		stdout, stderr := Structured(ctx)

		if stdout != &o.stdoutBuf {
			t.Errorf("got unexpected stdout")
		}

		if stderr != &o.stderrBuf {
			t.Errorf("got unexpected stderr")
		}
	})
}
