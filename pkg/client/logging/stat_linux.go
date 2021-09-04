package logging

import (
	"time"
)

func (u fileInfo) BirthTime() time.Time {
	return time.Unix(u.Ctim.Sec, u.Ctim.Nsec)
}
