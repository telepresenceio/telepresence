package ioutil

import "os"

// CreateTempName creates a new temporary file using os.CreateTemp, removes it, and then returns its name.
func CreateTempName(dir, pattern string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := f.Name()
	_ = f.Close()
	_ = os.Remove(path)
	return path, nil
}
