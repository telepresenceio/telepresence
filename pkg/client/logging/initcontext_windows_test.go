package logging

func dupStd() (func(), error) {
	return func() {}, nil
}
