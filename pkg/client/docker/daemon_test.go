package docker

import (
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
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
			if got := daemon.SafeContainerName(tt.name); got != tt.want {
				t.Errorf("SafeContainerName() = %v, want %v", got, tt.want)
			}
		})
	}
}
