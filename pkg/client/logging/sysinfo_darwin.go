package logging

import (
	"time"
)

func (u *unixSysInfo) birthtime() time.Time {
	sec, nsec := u.Birthtimespec.Unix()
	return time.Unix(sec, nsec)
}
