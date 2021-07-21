package dns

import (
	"context"
)

func Flush(c context.Context) {
	// Flush isn't needed here on windows. It's done by the `SetDNS()` on the TUN-device.
}
