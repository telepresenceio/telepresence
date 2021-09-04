package logging

import (
	"time"
)

func (u *unixSysInfo) BirthTime() time.Time {
	return time.Unix(u.Ctim.Sec, u.Ctim.Nsec)
}
