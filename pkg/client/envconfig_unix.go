//go:build !windows

package client

type OSSpecificEnv struct {
	Shell string `env:"SHELL" default:"/bin/bash"`
}
