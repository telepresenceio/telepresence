package userd_auth

import (
	"context"

	"golang.org/x/oauth2"
)

// cbTokenSource is an oauth2.TokenSource that calls a callback whenever a new token is issued or a
// new token fails to be issued.
type cbTokenSource struct {
	// Static
	inner oauth2.TokenSource
	ctx   context.Context // to pass to the callbacks
	tokCB func(context.Context, *oauth2.Token) error
	errCB func(context.Context, error) error

	// Stateful
	lastToken string
}

// Token implements oauth2.TokenSource.
func (ts *cbTokenSource) Token() (*oauth2.Token, error) {
	ret, err := ts.inner.Token()
	if (ret != nil) == (err != nil) {
		panic("TokenSource is broken")
	}
	if err != nil {
		err = ts.errCB(ts.ctx, err)
		return nil, err
	}
	if ret.AccessToken != ts.lastToken {
		if err := ts.tokCB(ts.ctx, ret); err != nil {
			return nil, err
		}
	}
	ts.lastToken = ret.AccessToken
	return ret, nil
}

// newTokenSource returns a new cbTokenSource.
func newTokenSource(
	// Static
	ctx context.Context,
	cfg oauth2.Config,
	tokCB func(context.Context, *oauth2.Token) error,
	errCB func(context.Context, error) error,
	// The initial token, must not be nil
	cur *oauth2.Token,
) oauth2.TokenSource {
	return &cbTokenSource{
		inner: cfg.TokenSource(ctx, cur),
		ctx:   ctx,
		tokCB: tokCB,
		errCB: errCB,

		lastToken: cur.AccessToken,
	}
}
