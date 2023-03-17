package client

type OSSpecificEnv struct {
	Shell string `env:"ComSpec, parser=nonempty-string,default=C:\\WINDOWS\\system32\\cmd.exe"`
}
