package client

import (
	"context"
	"io"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
)

// StdOutput sends everything written to its Stdout and Stderr to its
// ResultChannel. Things written to Stdout will have an error category
// of errcat.OK whereas Stderr with use errcat.NoDaemonLogs
type StdOutput interface {
	Stdout() io.Writer
	Stderr() io.Writer
	ResultChannel() <-chan *connector.StreamResult
}

type dispatchToCh struct {
	out    chan<- *connector.StreamResult
	errCat connector.Result_ErrorCategory
}

func (d dispatchToCh) Write(p []byte) (n int, err error) {
	n = len(p)
	if n > 0 {
		defer func() {
			// Don't bail out when writing on a closed stream
			if r := recover(); r != nil {
				if re, ok := r.(error); ok && re.Error() == "send on closed channel" {
					err = io.ErrClosedPipe
				} else {
					panic(r)
				}
			}
		}()

		// Need a private copy because the sender might reuse p in subsequent calls.
		cp := make([]byte, len(p))
		copy(cp, p)
		d.out <- &connector.StreamResult{
			Data: &connector.Result{
				Data:          cp,
				ErrorCategory: d.errCat,
			},
		}
	}
	return n, nil
}

type stdioHandler chan *connector.StreamResult

func (h stdioHandler) Stdout() io.Writer {
	return dispatchToCh{
		out:    h,
		errCat: 0,
	}
}

func (h stdioHandler) Stderr() io.Writer {
	return dispatchToCh{
		out:    h,
		errCat: connector.Result_NO_DAEMON_LOGS,
	}
}

func (h stdioHandler) ResultChannel() <-chan *connector.StreamResult {
	return h
}

// NewStdOutput returns a new instance of StdOutput which is closed when the
// given context is done.
func NewStdOutput(ctx context.Context) StdOutput {
	so := make(stdioHandler)
	go func() {
		<-ctx.Done()
		close(so)
	}()
	return so
}
