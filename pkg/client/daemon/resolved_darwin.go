package daemon

import "context"

func (o *outbound) tryResolveD(_ context.Context) error {
	return errResolveDNotConfigured
}
