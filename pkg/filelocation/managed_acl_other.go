//go:build !darwin && !windows

package filelocation

func aclWriter(string) (string, error) { return "", nil }
