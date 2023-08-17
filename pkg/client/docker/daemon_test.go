package docker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSafeContainerName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{
			"@",
			"a",
		},
		{
			"@x",
			"ax",
		},
		{
			"x@",
			"x_",
		},
		{
			"x@y",
			"x_y",
		},
		{
			"x™y", // multibyte char
			"x_y",
		},
		{
			"x™", // multibyte char
			"x_",
		},
		{
			"_y",
			"ay",
		},
		{
			"_y_",
			"ay_",
		},
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SafeContainerName(tt.name); got != tt.want {
				t.Errorf("SafeContainerName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAppendKubeFlagsAuthenticatorService(t *testing.T) {
	// when
	args, err := appendKubeFlags(map[string]string{
		"kubeconfig":               "/some/path/to/kubeconfig",
		"KUBECONFIG":               "/some/path/to/kubeconfig:/some/other/path/to/kubeconfig",
		"insecure-skip-tls-verify": "true",
		"as-group":                 "1000,1001",
		"user":                     "john",
	}, []string{"kubeauth-foreground"})

	// then
	assert.NoError(t, err)
	assert.Len(t, args, 10)
	argsStr := strings.Join(args, " ")
	assert.Contains(t, argsStr, "kubeauth-foreground")
	assert.Contains(t, argsStr, "--insecure-skip-tls-verify")
	assert.Contains(t, argsStr, "--as-group 1000 --as-group 1001")
	assert.Contains(t, argsStr, "--kubeconfig /some/path/to/kubeconfig")
	assert.Contains(t, argsStr, "--user john")
}
