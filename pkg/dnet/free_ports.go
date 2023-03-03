package dnet

import "net"

// FreePortsTCP uses net.Listen repeatedly to choose free TCP ports for the localhost. It then immediately closes
// the listeners and returns the addresses that were allocated.
//
// NOTE: Since the listeners are closed, there's a chance that someone else might allocate the returned addresses
// before they are actually used. The chances are slim though, since tests show that in most cases (at least on
// macOS and Linux), the same address isn't allocated for a while even if the allocation is made from different
// processes.
func FreePortsTCP(count int) ([]*net.TCPAddr, error) {
	ls := make([]net.Listener, 0, count)
	as := make([]*net.TCPAddr, count)
	defer func() {
		for _, l := range ls {
			_ = l.Close()
		}
	}()

	for i := 0; i < count; i++ {
		if l, err := net.Listen("tcp", "localhost:0"); err != nil {
			return nil, err
		} else {
			ls = append(ls, l)
			as[i] = l.Addr().(*net.TCPAddr)
		}
	}
	return as, nil
}
