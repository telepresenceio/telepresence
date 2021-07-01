package cliutil

import (
	"io"

	"github.com/spf13/cobra"
)

// SafeCobraCommand is more-or-less a subset of *cobra.Command, with less stuff exposed so I don't
// have to worry about things using it in ways they shouldn't.
type SafeCobraCommand interface {
	InOrStdin() io.Reader
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
	FlagError(error) error
}

type safeCobraCommandImpl struct {
	*cobra.Command
}

func NewSafeCobraCommand(cmd *cobra.Command) SafeCobraCommand {
	return &safeCobraCommandImpl{cmd}
}

func (w safeCobraCommandImpl) FlagError(err error) error {
	return w.Command.FlagErrorFunc()(w.Command, err)
}
