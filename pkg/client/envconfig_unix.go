//go:build !windows
// +build !windows

package client

type OSSpecificEnv struct {
	Shell string `env:"SHELL, parser=nonempty-string,default=/bin/bash"`
}
