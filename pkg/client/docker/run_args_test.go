package docker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageArg(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantImage string
		wantIndex int
	}{
		{"image only", []string{"img"}, "img", 0},
		{"no image", []string{"--rm", "-it"}, "", -1},
		{"empty", nil, "", -1},
		{"bool flags", []string{"--rm", "-it", "img", "cmd"}, "img", 2},
		{"long flag with value", []string{"--name", "x", "img"}, "img", 2},
		{"long flag with = value", []string{"--name=x", "img"}, "img", 1},
		{"short flag with value", []string{"-e", "A=B", "img"}, "img", 2},
		{"short bools before short flag with value", []string{"-itl", "a=b", "img"}, "img", 2},
		{"short flag with attached value", []string{"-p8080:8080", "img"}, "img", 1},
		{"attach", []string{"-a", "stdout", "img"}, "img", 2},
		{"cpu shares", []string{"-c", "512", "img"}, "img", 2},
		{"disable content trust", []string{"--disable-content-trust", "img"}, "img", 1},
		{"use api socket", []string{"--use-api-socket", "img"}, "img", 1},
		{"container args", []string{"--rm", "img", "-d", "--name", "x"}, "img", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			image, index := ImageArg(tt.args)
			require.Equal(t, tt.wantImage, image)
			require.Equal(t, tt.wantIndex, index)
		})
	}
}

func TestRunOptions(t *testing.T) {
	require.Equal(t, []string{"--rm", "-e", "A=B"}, RunOptions([]string{"--rm", "-e", "A=B", "img", "-addr", ":8080"}))
	require.Empty(t, RunOptions([]string{"img", "-it"}))
	require.Equal(t, []string{"--rm", "-it"}, RunOptions([]string{"--rm", "-it"}))
}
