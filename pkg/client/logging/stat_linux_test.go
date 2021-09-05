package logging_test

import (
	"errors"

	"golang.org/x/sys/unix"
)

func init() {
	var stat unix.Statx_t
	err := unix.Statx(-1, "/", 0, unix.STATX_BTIME, &stat)
	if err != nil && errors.Is(err, unix.ENOSYS) {
		osHasBTime = false
	}
}
