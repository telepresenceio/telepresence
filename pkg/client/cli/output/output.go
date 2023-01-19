// Package output provides structured output for *cobra.Command.
// Formatted output is enabled by setting the --output=[json|yaml] flag.
package output

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// Out returns an io.Writer that writes to the OutOrStdout of the current *cobra.Command, or
// if no command is active, to the os.Stdout. If formatted output is requested, the output
// will be delayed until Execute is called.
func Out(ctx context.Context) io.Writer {
	if cmd, ok := ctx.Value(key{}).(*cobra.Command); ok {
		return cmd.OutOrStdout()
	}
	return os.Stdout
}

// Err returns an io.Writer that writes to the ErrOrStderr of the current *cobra.Command, or
// if no command is active, to the os.Stderr. If formatted output is requested, the output
// will be delayed until Execute is called.
func Err(ctx context.Context) io.Writer {
	if cmd, ok := ctx.Value(key{}).(*cobra.Command); ok {
		return cmd.ErrOrStderr()
	}
	return os.Stderr
}

// Info is similar to Out, but if formatted output is requested, the output will be discarded.
//
// Info is primarily intended for messages that are not directly related to the command that
// executes, such as messages about starting up daemons or being connected to a context.
func Info(ctx context.Context) io.Writer {
	if cmd, ok := ctx.Value(key{}).(*cobra.Command); ok {
		if _, ok := cmd.OutOrStdout().(*output); ok {
			return io.Discard
		}
		return cmd.OutOrStdout()
	}
	return os.Stdout
}

// Object sets the object to be marshalled and printed on stdout when formatted output
// is requested using the `--output=<fmt>` flag. Otherwise, this function does nothing.
//
// If override is set to true, then the formatted output will consist solely of the given
// object. There will be no "cmd", "stdout", or "stderr" tags.
//
// The function will panic if data already has been written to the stdout of the command
// or if an Object already has been called.
func Object(ctx context.Context, obj any, override bool) {
	if cmd, ok := ctx.Value(key{}).(*cobra.Command); ok {
		if o, ok := cmd.OutOrStdout().(*output); ok {
			if o.Len() > 0 {
				panic("output.Object cannot be used together with output.Out")
			}
			if o.obj != nil {
				panic("output.Object can only be used once")
			}
			o.obj = obj
			o.override = override
		}
	}
}

// StreamObject prints an object immediately.
func StreamObject(ctx context.Context, obj any, override bool) {
	if cmd, ok := ctx.Value(key{}).(*cobra.Command); ok {
		if o, ok := cmd.OutOrStdout().(*output); ok {
			if !o.override {
				obj = o.shapeObjInfo(cmd, obj)
			}
			o.write(obj)
		}
	}
}

// DefaultYAML is a PersistentPRERunE function that will change the default output
// format to "yaml" for the command that invokes it.
func DefaultYAML(cmd *cobra.Command, _ []string) error {
	fmt, err := validateFlag(cmd)
	if err != nil {
		return err
	}
	rootCmd := cmd
	for {
		p := rootCmd.Parent()
		if p == nil {
			break
		}
		rootCmd = p
	}
	if fmt == formatDefault {
		if err = rootCmd.PersistentFlags().Set("output", "yaml"); err != nil {
			return err
		}
	}
	return rootCmd.PersistentPreRunE(cmd, cmd.Flags().Args())
}

// Execute will call ExecuteC on the given command, optionally print all formatted
// output, and return a boolean indicating if formatted output was printed. The
// result of the execution is provided in the second return value.
func Execute(cmd *cobra.Command) (*cobra.Command, bool, error) {
	setFormat(cmd)
	cmd, err := cmd.ExecuteC()
	o, ok := cmd.OutOrStdout().(*output)
	if !ok {
		return cmd, false, err
	}

	var obj any
	if err == nil && o.override {
		obj = o.obj
	} else {
		response := o.shapeObjInfo(cmd, o.obj)
		if err != nil {
			response.Err = err.Error()
		}
		// don't print out the "zero" object
		if response.hasCmdOnly() {
			return cmd, true, err
		}
		obj = response
	}

	o.write(obj)
	return cmd, true, err
}

func (o *output) shapeObjInfo(cmd *cobra.Command, obj any) *object {
	response := &object{
		Cmd: cmd.Name(),
	}
	if buf := o.Buffer; buf.Len() > 0 {
		response.Stdout = buf.String()
	} else if obj != nil {
		response.Stdout = obj
	}
	if buf, ok := cmd.ErrOrStderr().(*bytes.Buffer); ok && buf.Len() > 0 {
		response.Stderr = buf.String()
	}
	return response
}

// makeWriterForFormat creates an appropriate writer for the given format.
func makeWriterForFormat(format format, originalStdout io.Writer) func(obj any) {
	switch format {
	case formatJSON:
		// create the encoder once
		encoder := json.NewEncoder(originalStdout)
		return func(obj any) {
			if err := encoder.Encode(obj); err != nil {
				panic(err)
			}
		}
	case formatYAML:
		return func(obj any) {
			ym, err := yaml.Marshal(obj)
			if err == nil {
				_, err = originalStdout.Write(ym)
			}
			if err != nil {
				panic(err)
			}
		}
	default:
		return func(obj any) {
			fmt.Fprintf(originalStdout, "%+v", obj)
		}
	}
}

// setFormat assigns a cobra.Command.PersistentPreRunE function that all sub commands will inherit. This
// function checks if the global `--output` flag was used, and if so, ensures that formatted output is
// initialized.
func setFormat(cmd *cobra.Command) {
	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		fmt, err := validateFlag(cmd)
		if err != nil {
			return err
		}
		if fmt != formatDefault {
			o := output{
				write: makeWriterForFormat(fmt, cmd.OutOrStdout()),
			}
			cmd.SetOut(&o)
			cmd.SetErr(&bytes.Buffer{})
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
		}
		cmd.SetContext(context.WithValue(cmd.Context(), key{}, cmd))
		return nil
	}
}

// WantsFormatted returns true if the value of the global `--output` flag is set to a valid
// format different from "default".
func WantsFormatted(cmd *cobra.Command) bool {
	f, _ := validateFlag(cmd)
	return f != formatDefault
}

func validateFlag(cmd *cobra.Command) (format, error) {
	if of := cmd.Flags().Lookup("output"); of != nil && of.DefValue == "default" {
		fmt := strings.ToLower(of.Value.String())
		switch fmt {
		case "yaml":
			return formatYAML, nil
		case "json":
			return formatJSON, nil
		case "default":
			return formatDefault, nil
		default:
			return formatDefault, errcat.User.Newf("invalid output format %q", fmt)
		}
	}
	return formatDefault, nil
}

type (
	format int
	key    struct{}
	output struct {
		bytes.Buffer
		write    func(obj any)
		obj      any
		override bool
	}
	object struct {
		Cmd    string `json:"cmd"`
		Stdout any    `json:"stdout,omitempty"`
		Stderr any    `json:"stderr,omitempty"`
		Err    string `json:"err,omitempty"`
	}
)

const (
	formatDefault = format(iota)
	formatJSON
	formatYAML
)

func (o *output) Write(data []byte) (int, error) {
	if o.obj != nil {
		panic("Stdout cannot be used together with output.Object")
	}
	return o.Buffer.Write(data)
}

func (o *object) hasCmdOnly() bool {
	return o.Stdout == nil && o.Stderr == nil && o.Err == ""
}
