package ioutil

import "io"

// WriterTos implements something that can be represented as a list of io.WriterTo.
type WriterTos interface {
	WriterTos() []io.WriterTo
}

// WriteAllTo calls WriteTo on all elements of the WriterTo slice and
// returns the total number of bytes written.
func WriteAllTo(out io.Writer, wts ...io.WriterTo) (tn int64, err error) {
	for _, wt := range wts {
		if wt == nil {
			continue
		}
		var n int64
		if n, err = wt.WriteTo(out); err != nil {
			return tn, err
		}
		tn += n
	}
	return tn, nil
}
