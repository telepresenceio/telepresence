package dos

import (
	"context"
	"os"
)

// Exe is an abstraction of the executable related functions with the same name in the os package.
type Exe interface {
	Executable() (string, error)
}

type exeKey struct{}

func WithExe(ctx context.Context, exe Exe) context.Context {
	return context.WithValue(ctx, exeKey{}, exe)
}

// ExeAPI returns the Exe that has been registered with the given context, or
// the instance that delegates to the corresponding functions in the os package.
func ExeAPI(ctx context.Context) Exe {
	if e, ok := ctx.Value(exeKey{}).(Exe); ok {
		return e
	}
	return osExe{}
}

func Executable(ctx context.Context) (string, error) {
	return ExeAPI(ctx).Executable()
}

type osExe struct{}

func (osExe) Executable() (string, error) {
	return os.Executable()
}
