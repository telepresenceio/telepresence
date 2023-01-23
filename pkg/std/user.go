package std

import stduser "os/user"

type User struct{}

func (u *User) Current() (*stduser.User, error) {
	return stduser.Current()
}
