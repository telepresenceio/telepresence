// Package output provides structured output for *cobra.Command.
// Writing JSON to stdout is enable by setting the --output=json flag.
package output

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func WithStructure(ctx context.Context, cmd *cobra.Command) context.Context {
	next := cmd.PersistentPreRun
	o := output{
		stdout: cmd.OutOrStdout(),
		stderr: cmd.ErrOrStderr(),
	}

	cmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		// never output help messages as json
		if cmd.RunE == nil {
			return
		}

		cmd.RunE = o.runE(cmd.RunE)

		if next != nil {
			next(cmd, args)
		}
	}

	return context.WithValue(ctx, key{}, &o)
}

func Structured(ctx context.Context) (stdout, stderr io.Writer) {
	o, ok := ctx.Value(key{}).(*output)
	if !ok {
		return os.Stdout, os.Stderr
	}

	if !o.outputJSON {
		return os.Stdout, os.Stderr
	}

	return &o.stdoutBuf, &o.stderrBuf
}

type key struct{}
type output struct {
	cmd string

	stdoutBuf strings.Builder
	stderrBuf strings.Builder
	err       error

	nativeJSON bool
	outputJSON bool
	stdout     io.Writer
	stderr     io.Writer
}

func (o *output) runE(f func(cmd *cobra.Command, args []string) error) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		flagValue, _ := cmd.Flags().GetString("output")
		o.outputJSON = strings.ToLower(flagValue) == "json"
		if !o.outputJSON {
			return f(cmd, args)
		}

		nativeJSON, err := cmd.LocalFlags().GetBool("json")
		if err == nil {
			o.nativeJSON = nativeJSON
		}
		stdout := cmd.OutOrStdout()
		cmd.SetOut(&o.stdoutBuf)
		cmd.SetErr(&o.stderrBuf)
		o.cmd = cmd.Name()
		o.err = f(cmd, args)
		o.writeStructured(stdout)

		return nil
	}
}

func (o *output) writeStructured(w io.Writer) {
	response := struct {
		Cmd    string      `json:"cmd"`
		Err    string      `json:"err,omitempty"`
		Stdout interface{} `json:"stdout,omitempty"`
		Stderr string      `json:"stderr,omitempty"`
	}{
		Cmd:    o.cmd,
		Stdout: o.stdoutBuf.String(),
		Stderr: o.stderrBuf.String(),
	}

	if o.err != nil {
		response.Err = o.err.Error()
	}
	if o.nativeJSON {
		response.Stdout = json.RawMessage(response.Stdout.(string))
	}

	_ = json.NewEncoder(w).Encode(response)
}
