package output

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
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
		cmd.SetContext(context.Background())
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
