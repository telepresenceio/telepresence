package scout

import "context"

type reporterKey struct{}

func WithReporter(ctx context.Context, reporter Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, reporter)
}

func getReporter(ctx context.Context) Reporter {
	if r, ok := ctx.Value(reporterKey{}).(Reporter); ok {
		return r
	}
	return nil
}
