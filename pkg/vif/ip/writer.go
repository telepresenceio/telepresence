package ip

import "context"

type Writer interface {
	Write(context.Context, Packet) error
}
