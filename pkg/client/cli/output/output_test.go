package output

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func TestWithOutput(t *testing.T) {
	expectedREStdout := "re\n"
	expectedREStderr := "re_stderr\n"
	expectedName := "testing"

	re := func(cmd *cobra.Command, args []string) error {
		stdout := cmd.OutOrStdout()
		stderr := cmd.ErrOrStderr()
		ioutil.Print(stdout, expectedREStdout)
		ioutil.Print(stderr, expectedREStderr)
		return nil
	}

	newCmdWithBufs := func() (*cobra.Command, *strings.Builder, *strings.Builder) {
		stdoutBuf := strings.Builder{}
		stderrBuf := strings.Builder{}
		cmd := cobra.Command{}

		cmd.Use = expectedName
		cmd.SetOut(&stdoutBuf)
		cmd.SetErr(&stderrBuf)
		cmd.SetContext(context.Background())
		cmd.PersistentPreRunE = SetFormat
		cmd.RunE = re

		cmd.PersistentFlags().String(global.FlagOutput, "default", "")

		return &cmd, &stdoutBuf, &stderrBuf
	}

	t.Run("non-json output", func(t *testing.T) {
		cmd, outBuf, errBuf := newCmdWithBufs()
		_, _, err := Execute(cmd)
		require.NoError(t, err)

		require.Equal(t, expectedREStdout, outBuf.String(), "did not get expected stdout")
		require.Equal(t, expectedREStderr, errBuf.String(), "did not get expected stderr")
	})

	t.Run("json output no error", func(t *testing.T) {
		cmd, outBuf, errBuf := newCmdWithBufs()
		cmd.SetArgs([]string{"--output=json"})
		_, _, err := Execute(cmd)
		require.NoError(t, err)

		stdout := outBuf.String()
		m := map[string]string{}
		require.NoError(t, json.Unmarshal([]byte(stdout), &m), "did not get json as stdout, got: %s", stdout)
		require.Equal(t, expectedREStdout, m["stdout"], "did not get expected stdout, got: %s", m["stdout"])
		require.Equal(t, expectedREStderr, m["stderr"], "did not get expected stderr, got: %s", m["stderr"])
		require.Equal(t, expectedName, m["cmd"], "did not get expected cmd name, got: %s", m["cmd"])

		stderr := errBuf.String()
		require.Empty(t, stderr, "expected empty stderr, got: %s", stderr)
	})

	t.Run("json output with error", func(t *testing.T) {
		expectedErr := "ERROR"
		cmd, outBuf, _ := newCmdWithBufs()
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			return errors.New(expectedErr)
		}
		cmd.SetArgs([]string{"--output=json"})
		_, _, err := Execute(cmd)
		require.Error(t, err)

		stdout := outBuf.String()
		m := map[string]string{}
		require.NoError(t, json.Unmarshal([]byte(stdout), &m), "did not get json as stdout, got: %s", stdout)
		require.Equal(t, expectedErr, m["err"], "did not get expected err, got: %s", m["err"])
	})

	t.Run("yaml output with error", func(t *testing.T) {
		expectedErr := "ERROR"
		cmd, outBuf, _ := newCmdWithBufs()
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			return errors.New(expectedErr)
		}
		cmd.SetArgs([]string{"--output=yaml"})
		_, _, err := Execute(cmd)
		require.Error(t, err)

		stdout := outBuf.String()
		m := map[string]string{}
		require.NoError(t, yaml.Unmarshal([]byte(stdout), &m), "did not get yaml as stdout, got: %s", stdout)
		require.Equal(t, expectedErr, m["err"], "did not get expected err, got: %s", m["err"])
	})

	t.Run("json output with native json", func(t *testing.T) {
		expectedNativeJSONMap := map[string]float64{
			"a": 1,
		}
		cmd, outBuf, errBuf := newCmdWithBufs()
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			Object(cmd.Context(), expectedNativeJSONMap, false)
			return nil
		}
		cmd.SetArgs([]string{"--output=json"})
		_, _, err := Execute(cmd)
		require.NoError(t, err)

		stdout := outBuf.String()
		m := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(stdout), &m), "did not get json as stdout, got: %s", stdout)
		jsonOutputBytes, err := json.Marshal(m["stdout"])
		require.NoError(t, err, "did not get json stdout as expected")
		expectedJSONOutputBytes, _ := json.Marshal(expectedNativeJSONMap)

		require.Equal(t, expectedJSONOutputBytes, jsonOutputBytes, "did not get expected stdout json")
		stderr := errBuf.String()
		require.Empty(t, stderr, "expected empty stderr, got: %s", stderr)
	})

	t.Run("json output with native json and other output", func(t *testing.T) {
		expectedNativeJSONMap := map[string]float64{
			"a": 1,
		}
		cmd, _, _ := newCmdWithBufs()
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			Object(cmd.Context(), expectedNativeJSONMap, false)
			fmt.Fprintln(cmd.OutOrStdout(), "hello")
			return nil
		}
		cmd.SetArgs([]string{"--output=json"})
		require.Panics(t, func() {
			_, _, _ = Execute(cmd)
		})
	})

	t.Run("json output with other output and native json", func(t *testing.T) {
		expectedNativeJSONMap := map[string]float64{
			"a": 1,
		}
		cmd, _, _ := newCmdWithBufs()
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "hello")
			Object(cmd.Context(), expectedNativeJSONMap, false)
			return nil
		}
		cmd.SetArgs([]string{"--output=json"})
		require.Panics(t, func() {
			_, _, _ = Execute(cmd)
		})
	})

	t.Run("json output with multiple native json", func(t *testing.T) {
		expectedNativeJSONMap := map[string]float64{
			"a": 1,
		}
		cmd, _, _ := newCmdWithBufs()
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			Object(cmd.Context(), expectedNativeJSONMap, false)
			Object(cmd.Context(), expectedNativeJSONMap, false)
			return nil
		}
		cmd.SetArgs([]string{"--output=json"})
		require.Panics(t, func() {
			_, _, _ = Execute(cmd)
		})
	})

	t.Run("json output with overriding native json", func(t *testing.T) {
		expectedNativeJSONMap := map[string]float64{
			"a": 1,
		}
		cmd, outBuf, _ := newCmdWithBufs()
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			Object(cmd.Context(), expectedNativeJSONMap, true)
			return nil
		}
		cmd.SetArgs([]string{"--output=json"})
		_, _, err := Execute(cmd)
		require.NoError(t, err)

		stdout := outBuf.String()
		m := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(stdout), &m), "did not get json as stdout, got: %s", stdout)
		require.Equal(t, 1.0, m["a"])
	})

	t.Run("json output with overriding native json and error", func(t *testing.T) {
		expectedNativeMap := map[string]any{
			"a": 1.0,
		}
		cmd, outBuf, _ := newCmdWithBufs()
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			Object(cmd.Context(), expectedNativeMap, true)
			return errors.New("this went south")
		}
		cmd.SetArgs([]string{"--output=json"})
		_, _, err := Execute(cmd)
		require.Error(t, err)

		stdout := outBuf.String()
		m := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(stdout), &m), "did not get json as stdout, got: %s", stdout)
		require.Equal(t, m["stdout"], expectedNativeMap, "did not get expected stdout")
		require.Empty(t, m["stderr"], "did not get empty stderr")
		require.Equal(t, m["err"], "this went south")
	})
}

func TestWithFormat(t *testing.T) {
	const expectedName = "testing"

	newCmd := func() (*cobra.Command, *strings.Builder) {
		outBuf := strings.Builder{}
		cmd := cobra.Command{Use: expectedName}
		cmd.SetOut(&outBuf)
		cmd.SetErr(&strings.Builder{})
		cmd.SetContext(context.Background())
		cmd.PersistentPreRunE = SetFormat
		cmd.PersistentFlags().String(global.FlagOutput, "default", "")
		cmd.PersistentFlags().String(global.FlagFormat, "default", "")
		return &cmd, &outBuf
	}

	t.Run("--format json emits a clean object, no envelope", func(t *testing.T) {
		cmd, outBuf := newCmd()
		// override=false: the legacy --output would wrap this, but --format must not.
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			Object(cmd.Context(), map[string]any{"a": 1.0}, false)
			return nil
		}
		cmd.SetArgs([]string{"--format=json"})
		_, fmtOutput, err := Execute(cmd)
		require.NoError(t, err)
		require.True(t, fmtOutput)

		m := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(outBuf.String()), &m))
		require.Equal(t, 1.0, m["a"])
		require.NotContains(t, m, "cmd")
		require.NotContains(t, m, "stdout")
	})

	t.Run("--format json on error emits a clean error object", func(t *testing.T) {
		cmd, outBuf := newCmd()
		cmd.RunE = func(*cobra.Command, []string) error { return errors.New("boom") }
		cmd.SetArgs([]string{"--format=json"})
		_, fmtOutput, err := Execute(cmd)
		require.Error(t, err)
		require.True(t, fmtOutput)

		m := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(outBuf.String()), &m))
		require.Equal(t, "boom", m["error"])
		require.NotContains(t, m, "cmd")
		require.NotContains(t, m, "err")
	})

	t.Run("--format json on a text-only command falls back to a JSON string", func(t *testing.T) {
		cmd, outBuf := newCmd()
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			ioutil.Print(cmd.OutOrStdout(), "plain text")
			return nil
		}
		cmd.SetArgs([]string{"--format=json"})
		_, _, err := Execute(cmd)
		require.NoError(t, err)

		var s string
		require.NoError(t, json.Unmarshal([]byte(outBuf.String()), &s))
		require.Equal(t, "plain text", s)
	})

	t.Run("--output and --format together is an error", func(t *testing.T) {
		cmd, _ := newCmd()
		cmd.RunE = func(*cobra.Command, []string) error { return nil }
		cmd.SetArgs([]string{"--output=json", "--format=json"})
		_, fmtOutput, err := Execute(cmd)
		require.Error(t, err)
		require.False(t, fmtOutput)
		require.Contains(t, err.Error(), "cannot be used together")
	})

	t.Run("a local --format flag shadows the global one and is left untouched", func(t *testing.T) {
		cmd, outBuf := newCmd()
		// A command-local --format (DefValue != "default"), like docker compose's passthrough.
		var local string
		cmd.Flags().StringVar(&local, global.FlagFormat, "table", "")
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			ioutil.Print(cmd.OutOrStdout(), "from compose")
			return nil
		}
		cmd.SetArgs([]string{"--format=table"})
		_, fmtOutput, err := Execute(cmd)
		require.NoError(t, err)
		require.False(t, fmtOutput, "global machinery must not activate for a local --format")
		require.Equal(t, "table", local, "local --format must receive the value")
		require.Equal(t, "from compose", outBuf.String(), "output must be unchanged plain text")
	})
}
