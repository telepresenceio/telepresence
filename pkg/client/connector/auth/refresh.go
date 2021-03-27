package auth

import (
	"context"

	"golang.org/x/oauth2"
)

type cbTokenSource struct {
	inner oauth2.TokenSource
	cb    func(context.Context, *oauth2.Token) error
	ctx   context.Context

	lastToken string
}

func (ts *cbTokenSource) Token() (*oauth2.Token, error) {
	ret, err := ts.inner.Token()
	if err != nil {
		return nil, err
	}
	if ret.AccessToken != ts.lastToken {
		if err := ts.cb(ts.ctx, ret); err != nil {
			return nil, err
		}
	}
	ts.lastToken = ret.AccessToken
	return ret, nil
}

func newTokenSource(ctx context.Context, cfg oauth2.Config, cur *oauth2.Token, cbNow bool, cb func(context.Context, *oauth2.Token) error) (oauth2.TokenSource, error) {
	ret := &cbTokenSource{
		inner: cfg.TokenSource(ctx, cur),
		cb:    cb,
		ctx:   ctx,

		lastToken: cur.AccessToken,
	}
	if cbNow {
		if err := cb(ctx, cur); err != nil {
			return nil, err
		}
	}
	return ret, nil
}
