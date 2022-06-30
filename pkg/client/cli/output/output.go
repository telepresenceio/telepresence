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
	"github.com/spf13/pflag"
)

func WithStructure(ctx context.Context, cmd *cobra.Command) context.Context {
	next := cmd.PersistentPreRun
	o := output{
		originalStdout: cmd.OutOrStdout(),
		originalStderr: cmd.ErrOrStderr(),
	}
	o.stdout = o.originalStdout
	o.stderr = o.originalStderr

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
	o, _ := ctx.Value(key{}).(*output)
	if o == nil {
		return os.Stdout, os.Stderr
	}

	return o.stdout, o.stderr
}

func SetJSONStdout(ctx context.Context) {
	o, _ := ctx.Value(key{}).(*output)
	if o == nil {
		return
	}

	o.stdoutIsJSON = true
}

func SetJSONStderr(ctx context.Context) {
	o, _ := ctx.Value(key{}).(*output)
	if o != nil {
		return
	}

	o.stdoutIsJSON = true
}

type key struct{}
type output struct {
	cmd string

	stdoutBuf strings.Builder
	stderrBuf strings.Builder

	stdoutIsJSON bool
	stderrIsJSON bool

	jsonEncoder *json.Encoder
	stdout      io.Writer
	stderr      io.Writer

	originalStdout io.Writer
	originalStderr io.Writer
}

func (o *output) runE(f func(cmd *cobra.Command, args []string) error) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if !WantsJSONOutput(cmd.Flags()) {
			return f(cmd, args)
		}

		o.cmd = cmd.Name()
		o.jsonEncoder = json.NewEncoder(o.originalStdout)
		o.stdout = &o.stdoutBuf
		o.stderr = &o.stderrBuf

		cmd.SetOut(&streamerWriter{
			output: o,
		})
		cmd.SetErr(&streamerWriter{
			output:   o,
			isStderr: true,
		})

		err := f(cmd, args)
		o.writeStructured(err)

		return nil
	}
}

func (o *output) writeStructured(err error) {
	response := object{
		Cmd: o.cmd,
	}

	if buf := o.stdoutBuf; 0 < buf.Len() {
		if o.stdoutIsJSON {
			response.Stdout = json.RawMessage(buf.String())
		} else {
			response.Stdout = buf.String()
		}
	}
	if buf := o.stderrBuf; 0 < buf.Len() {
		if o.stderrIsJSON {
			response.Stderr = json.RawMessage(buf.String())
		} else {
			response.Stderr = buf.String()
		}
	}
	if err != nil {
		response.Err = err.Error()
	}

	// dont print out the "zero" object
	if response.hasCmdOnly() {
		return
	}

	_ = o.jsonEncoder.Encode(response)
}

func WantsJSONOutput(flags *pflag.FlagSet) bool {
	flagValue, _ := flags.GetString("output")
	return strings.ToLower(flagValue) == "json"
}

type object struct {
	Cmd    string `json:"cmd"`
	Err    string `json:"err,omitempty"`
	Stdout any    `json:"stdout,omitempty"`
	Stderr any    `json:"stderr,omitempty"`
}

func (o *object) hasCmdOnly() bool {
	x := o.Err == ""
	x = x && o.Stdout == nil
	x = x && o.Stderr == nil
	return x
}
