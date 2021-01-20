package daemon

import (
	"context"
)

func (o *outbound) dnsServerWorker(c context.Context) error {
	return o.runLocalServer(c)
}
