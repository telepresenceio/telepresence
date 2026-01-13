package client

type OSSpecificEnv struct {
	Shell string `env:"ComSpec" default:"C:\\WINDOWS\\system32\\cmd.exe"`
}
