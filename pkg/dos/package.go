// Package dos contains an abstraction of some functions in the go os package. When used in code, it allows those
// functions to be mocked in unit tests.
// In general, the functions are implemented using an interface which is then stored in the context. The functions
// are then called using dos instead of os, and with an additional first context argument. E.g.
//
//     ctx := dos.WithFS(ctx, mockFS)
//     f, err := dos.Open(ctx, "/etc/resolv.conf")
package dos
