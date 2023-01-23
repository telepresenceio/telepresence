package std

import stdos "os"

type OS struct{}

func (o *OS) Hostname() (string, error) {
	return stdos.Hostname()
}
