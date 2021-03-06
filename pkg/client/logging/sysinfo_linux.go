package logging

import (
	"time"
)

func (u *unixSysInfo) birthtime() time.Time {
	return time.Unix(u.Ctim.Sec, u.Ctim.Nsec)
}
