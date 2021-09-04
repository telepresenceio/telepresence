package logging

import (
	"time"
)

func (u fileInfo) BirthTime() time.Time {
	sec, nsec := u.Birthtimespec.Unix()
	return time.Unix(sec, nsec)
}
