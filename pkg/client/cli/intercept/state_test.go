package intercept

import (
	"strings"
	"testing"
)

func Test_getArg(t *testing.T) {
	tests := []struct {
		args    []string
		flag    string
		wantV   string
		wantErr bool
	}{
		{
			[]string{"--name="},
			"--name",
			"",
			true,
		},
		{
			[]string{"--name"},
			"--name",
			"",
			true,
		},
		{
			[]string{"--name", "--other"},
			"--name",
			"",
			true,
		},
		{
			[]string{"--name", "-o"},
			"--name",
			"",
			true,
		},
		{
			[]string{"--name=value"},
			"--name",
			"value",
			false,
		},
		{
			[]string{"--name=-value-"},
			"--name",
			"-value-",
			false,
		},
		{
			[]string{"--name", "value"},
			"--name",
			"value",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			gotV, err := getArg(tt.args, tt.flag)
			if (err != nil) != tt.wantErr {
				t.Errorf("getArg() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotV != tt.wantV {
				t.Errorf("getArg() gotV = %v, want %v", gotV, tt.wantV)
			}
		})
	}
}
