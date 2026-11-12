package docker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFlags_Validate_detach(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"short detach", []string{"--rm", "-d", "img"}, true},
		{"combined short detach", []string{"-dit", "img"}, true},
		{"long detach", []string{"--detach", "img"}, true},
		{"container arg containing d", []string{"--rm", "img", "/server", "-addr", ":8080"}, false},
		{"container arg -d", []string{"img", "-d"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Flags{Run: true}
			err := f.Validate(tt.args)
			if tt.wantErr {
				require.ErrorContains(t, err, "detach")
			} else {
				require.NoError(t, err)
				require.Equal(t, "img", f.Image)
			}
		})
	}
}

func TestFlags_GetContainerNameAndArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantArgs []string
	}{
		{
			"name before image",
			[]string{"--name", "mine", "img"},
			"mine",
			[]string{"--name", "mine", "img"},
		},
		{
			"name in container args",
			[]string{"--rm", "img", "app", "--name", "other"},
			"default",
			[]string{"--name", "default", "--rm", "img", "app", "--name", "other"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Flags{Run: true}
			require.NoError(t, f.Validate(tt.args))
			name, args, err := f.GetContainerNameAndArgs("default")
			require.NoError(t, err)
			require.Equal(t, tt.wantName, name)
			require.Equal(t, tt.wantArgs, args)
			require.Equal(t, "img", args[f.imageIndex])
		})
	}
}
