package daemon

import (
	"context"
)

func (o *outbound) dnsServerWorker(c context.Context) error {
	// No representation of DNS server for MacOS in this commit
	return nil
}
