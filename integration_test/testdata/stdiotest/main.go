package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

type runner struct {
	useStdErr bool
	noNL      bool
}

func main() {
	r := runner{}
	cmd := cobra.Command{
		Use:   "stdiotest",
		Short: "Test stdin, stdout, and stderr",
		Long:  "stdiotest will echo either echo its arguments, or when no arguments are given, its stdin",
		RunE:  r.run,
	}
	flags := cmd.Flags()
	flags.BoolVarP(&r.useStdErr, "stderr", "e", false, "send output to stderr (default is stdout)")
	flags.BoolVarP(&r.useStdErr, "nonl", "n", false, "don't append a newline to each echoed argument (invalid when echoing stdin)")
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func (r runner) run(cmd *cobra.Command, args []string) error {
	var out io.Writer
	if r.useStdErr {
		out = cmd.ErrOrStderr()
	} else {
		out = cmd.OutOrStdout()
	}
	if len(args) == 0 {
		_, err := io.Copy(out, cmd.InOrStdin())
		return err
	}

	if r.noNL {
		for _, arg := range args {
			if _, err := out.Write([]byte(arg)); err != nil {
				return err
			}
		}
	} else {
		for _, arg := range args {
			if _, err := fmt.Fprintln(out, arg); err != nil {
				return err
			}
		}
	}
	return nil
}
