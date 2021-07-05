package logging

import (
	"time"
)

func (u *unixSysInfo) Birthtime() time.Time {
	return time.Unix(u.Ctim.Sec, u.Ctim.Nsec)
}
