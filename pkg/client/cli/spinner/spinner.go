package spinner

import "context"

type Spinner interface {
	Helper()

	// Start spinning the spinner.
	Start()

	// Done stops the spinner and prints a success icon.
	Done()

	// DoneMsg stops the spinner and sets the success icon with a new message.
	DoneMsg(msg string)

	// Error prints the error to the spinner, stops the spinner, then returns the error
	// so that you can continue passing it if needed.
	Error(err error) error

	// Message writes a message to the current spinner displayed alongside the initial job message.
	Message(msg string)
}

type Provider interface {
	New(string) Spinner
}

type key struct{}

func WithProvider(ctx context.Context, sp Provider) context.Context {
	return context.WithValue(ctx, key{}, sp)
}

// New configures a new spinner with the default values displaying the job message.
func New(ctx context.Context, job string) Spinner {
	sp, ok := ctx.Value(key{}).(Provider)
	if !ok {
		return noop{}
	}
	spin := sp.New(job)
	spin.Helper()
	spin.Start()
	return spin
}

type noop struct{}

func (n noop) Helper() {
}

func (n noop) Start() {
}

func (n noop) Done() {
}

func (n noop) DoneMsg(string) {
}

func (n noop) Error(error) error {
	return nil
}

func (n noop) Message(string) {
}
