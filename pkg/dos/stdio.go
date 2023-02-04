package dos

import (
	"context"
	"io"
	"os"
)

type Stdio interface {
	InOrStdin() io.Reader
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
}

// WithStdio returns a new context that is parented by the given context, that will return the values
// from the given Stdio when used as an argument to in a call to Stdin, Stdout, or Stderr.
func WithStdio(ctx context.Context, io Stdio) context.Context {
	ctx = WithStdin(ctx, io.InOrStdin())
	ctx = WithStdout(ctx, io.OutOrStdout())
	return WithStderr(ctx, io.ErrOrStderr())
}

type outKey struct{}

// Stdout returns the context's stdout io.Writer. If no such writer has been defined, it returns os.Stdout.
func Stdout(ctx context.Context) io.Writer {
	if w, ok := ctx.Value(outKey{}).(io.Writer); ok {
		return w
	}
	return os.Stdout
}

// WithStdout returns a new context that is parented by the given context, that will return the given writer
// when used as an argument to in a call to Stdout.
func WithStdout(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, outKey{}, w)
}

type errKey struct{}

// Stderr returns the context's stdout io.Writer. If no such writer has been defined, it returns os.Stderr.
func Stderr(ctx context.Context) io.Writer {
	if w, ok := ctx.Value(errKey{}).(io.Writer); ok {
		return w
	}
	return os.Stderr
}

// WithStderr returns a new context that is parented by the given context, that will return the given writer
// when used as an argument to in a call to Stderr.
func WithStderr(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, errKey{}, w)
}

type inKey struct{}

// Stdin returns the context's stdin io.Reader. If no such reader has been defined, it returns os.Stdin.
func Stdin(ctx context.Context) io.Reader {
	if r, ok := ctx.Value(inKey{}).(io.Reader); ok {
		return r
	}
	return os.Stdin
}

// WithStdin returns a new context that is parented by the given context, that will return the given reader
// when used as an argument to in a call to Stdin.
func WithStdin(ctx context.Context, w io.Reader) context.Context {
	return context.WithValue(ctx, inKey{}, w)
}
