// Package output provides structured output for *cobra.Command.
//
// Two global flags request formatted output:
//
//   - --format=[json|yaml|json-stream] (preferred) produces a clean structured
//     object with no "cmd"/"stdout"/"stderr"/"err" envelope.
//   - --output=[json|yaml] (deprecated) is retained for backward compatibility.
//     It wraps text-producing commands in a {cmd, stdout, stderr, err} object.
//
// The two flags are mutually exclusive. Only the global flags participate; a
// command that defines its own local flag of the same name (for example the
// docker compose passthrough --format, or genyaml's --output file flag) shadows
// the global flag, and is left untouched. The global flags are recognized by
// their default value "default".
package output

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
)

// Out returns an io.Writer that writes to the OutOrStdout of the current *cobra.Command, or
// if no command is active, to the os.Stdout. If formatted output is requested, the output
// will be delayed until Execute is called.
func Out(ctx context.Context) io.Writer {
	if cmd, ok := ctx.Value(key{}).(*cobra.Command); ok {
		return cmd.OutOrStdout()
	}
	return dos.Stdout(ctx)
}

// Err returns an io.Writer that writes to the ErrOrStderr of the current *cobra.Command, or
// if no command is active, to the os.Stderr. If formatted output is requested, the output
// will be delayed until Execute is called.
func Err(ctx context.Context) io.Writer {
	if cmd, ok := ctx.Value(key{}).(*cobra.Command); ok {
		return cmd.ErrOrStderr()
	}
	return dos.Stderr(ctx)
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
	return dos.Stdout(ctx)
}

// Object sets the object to be marshalled and printed on stdout when formatted output
// is requested using the `--format` or `--output` flag. Otherwise, this function does nothing.
//
// If override is set to true, then formatted output produced for the deprecated `--output`
// flag will consist solely of the given object. There will be no "cmd", "stdout", or "stderr"
// tags. The `--format` flag always produces such clean output, regardless of override.
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

			if o.format == formatJSONStream {
				data, err := json.Marshal(obj)
				if err == nil {
					_, err = o.originalStdout.Write(data)
				}
				if err != nil {
					panic(err)
				}
			} else {
				o.obj = obj
			}

			o.override = override
		}
	}
}

// DefaultYAML is a PersistentPRERunE function that will change the default output
// format to "yaml" for the command that invokes it.
func DefaultYAML(cmd *cobra.Command, _ []string) error {
	fmt, _, err := resolveFormat(cmd)
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
		if err = rootCmd.PersistentFlags().Set(global.FlagFormat, "yaml"); err != nil {
			return err
		}
	}
	return rootCmd.PersistentPreRunE(cmd, cmd.Flags().Args())
}

// Execute will call ExecuteC on the given command, optionally print all formatted
// output, and return a boolean indicating if formatted output was printed. The
// result of the execution is provided in the second return value.
func Execute(cmd *cobra.Command) (*cobra.Command, bool, error) {
	cmd, err := cmd.ExecuteC()
	o, ok := cmd.OutOrStdout().(*output)
	if !ok {
		return cmd, false, err
	}

	var obj any
	switch {
	case o.cleanEnvelope:
		// --format: never wrap in the {cmd, stdout, stderr, err} envelope.
		switch {
		case err != nil:
			obj = errObject{Error: err.Error()}
		case o.obj != nil:
			obj = o.obj
		case o.Len() > 0:
			// Fallback for commands that still only emit text; their output is
			// returned as a bare JSON string rather than wrapped.
			obj = o.String()
		default:
			return cmd, true, err
		}
	case err == nil && o.override:
		obj = o.obj
	default:
		response := &object{
			Cmd: cmd.Name(),
		}
		if buf := o.Buffer; buf.Len() > 0 {
			response.Stdout = buf.String()
		} else if o.obj != nil {
			response.Stdout = o.obj
		}
		if buf, ok := cmd.ErrOrStderr().(*bytes.Buffer); ok && buf.Len() > 0 {
			response.Stderr = buf.String()
		}
		if err != nil {
			response.Err = err.Error()
		}
		// don't print out the "zero" object
		if response.hasCmdOnly() {
			return cmd, true, err
		}
		obj = response
	}

	switch o.format {
	case formatJSON:
		data, encErr := json.Marshal(obj)
		if encErr == nil {
			_, encErr = o.originalStdout.Write(data)
		}
		if encErr != nil {
			panic(encErr)
		}
	case formatYAML:
		ym, encErr := json.Marshal(obj)
		if encErr == nil {
			ym, encErr = yaml.JSONToYAML(ym)
			if encErr == nil {
				_, encErr = o.originalStdout.Write(ym)
			}
		}
		if encErr != nil {
			panic(encErr)
		}
	case formatJSONStream:
		// Success objects are streamed immediately by Object. Only a clean
		// error object (or a text fallback) under --format remains to emit.
		if o.cleanEnvelope {
			data, encErr := json.Marshal(obj)
			if encErr == nil {
				_, encErr = o.originalStdout.Write(data)
			}
			if encErr != nil {
				panic(encErr)
			}
		}
	default:
		fmt.Fprintf(o.originalStdout, "%+v", obj)
	}
	return cmd, true, err
}

// SetFormat assigns a cobra.Command.PersistentPreRunE function that all sub commands will inherit. This
// function checks if the global `--format` or `--output` flag was used, and if so, ensures that formatted
// output is initialized.
func SetFormat(cmd *cobra.Command, _ []string) error {
	err := global.InitConfig(cmd)
	if err != nil {
		return err
	}
	fmt, cleanEnvelope, err := resolveFormat(cmd)
	if err != nil {
		return err
	}
	// Warn (on the real stderr, so it never corrupts structured stdout) when the
	// deprecated global --output flag is used.
	if of := globalFlag(cmd, global.FlagOutput); of != nil && of.Changed {
		_, _ = io.WriteString(dos.Stderr(cmd.Context()),
			"Flag --output has been deprecated, use --format instead\n")
	}
	if fmt != formatDefault {
		o := output{
			format:         fmt,
			cleanEnvelope:  cleanEnvelope,
			originalStdout: cmd.OutOrStdout(),
		}
		cmd.SetOut(&o)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
	}
	cmd.SetContext(context.WithValue(cmd.Context(), key{}, cmd))
	return nil
}

// WantsFormatted returns true if a valid format different from "default" was requested
// through either the `--format` or the `--output` flag.
func WantsFormatted(cmd *cobra.Command) bool {
	f, _, _ := resolveFormat(cmd)
	return f != formatDefault
}

// WantsStream returns true if the requested format is "json-stream".
func WantsStream(cmd *cobra.Command) bool {
	f, _, _ := resolveFormat(cmd)
	return f == formatJSONStream
}

// WantsClean returns true if envelope-free structured output was requested through the
// `--format` flag (as opposed to the deprecated `--output` flag, which wraps text-producing
// commands in a {cmd, stdout, ...} object). Commands that historically only emitted text
// should use this to decide whether to produce a structured object via Object: emit the
// object only when WantsClean is true so that their `--output` shape stays unchanged.
func WantsClean(cmd *cobra.Command) bool {
	f, clean, _ := resolveFormat(cmd)
	return clean && f != formatDefault
}

// globalFlag returns the named flag only when it is the global flag, recognized by its
// "default" default value. A command that defines its own local flag of the same name
// (which shadows the inherited global flag) is thereby left untouched.
func globalFlag(cmd *cobra.Command, name string) *pflag.Flag {
	if f := cmd.Flags().Lookup(name); f != nil && f.DefValue == "default" {
		return f
	}
	return nil
}

// resolveFormat determines the effective output format and whether it was requested through
// the `--format` flag (cleanEnvelope == true) rather than the deprecated `--output` flag.
// It is an error to set both flags.
func resolveFormat(cmd *cobra.Command) (format, bool, error) {
	ff := globalFlag(cmd, global.FlagFormat)
	of := globalFlag(cmd, global.FlagOutput)
	fSet := ff != nil && ff.Changed
	oSet := of != nil && of.Changed
	if fSet && oSet {
		return formatDefault, false, errcat.User.New(
			"--output and --format cannot be used together; --output is deprecated, use --format instead")
	}
	if fSet {
		f, err := parseFormat(ff.Value.String())
		return f, true, err
	}
	if oSet {
		f, err := parseFormat(of.Value.String())
		return f, false, err
	}
	return formatDefault, false, nil
}

func parseFormat(s string) (format, error) {
	switch strings.ToLower(s) {
	case "yaml":
		return formatYAML, nil
	case "json":
		return formatJSON, nil
	case "json-stream":
		return formatJSONStream, nil
	case "default":
		return formatDefault, nil
	default:
		return formatDefault, errcat.User.Newf("invalid output format %q", s)
	}
}

type (
	format int
	key    struct{}
	output struct {
		bytes.Buffer
		format         format
		obj            any
		override       bool
		cleanEnvelope  bool
		originalStdout io.Writer
	}
	object struct {
		Cmd    string `json:"cmd"`
		Stdout any    `json:"stdout,omitempty"`
		Stderr any    `json:"stderr,omitempty"`
		Err    string `json:"err,omitempty"`
	}
	errObject struct {
		Error string `json:"error"`
	}
)

const (
	formatDefault = format(iota)
	formatJSON
	formatYAML
	formatJSONStream
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
