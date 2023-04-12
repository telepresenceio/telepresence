package flags

import (
	"strings"
	"testing"
)

func TestGetUnparsedFlagValue(t *testing.T) {
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
			gotV, err := GetUnparsedValue(tt.args, tt.flag)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetUnparsedValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotV != tt.wantV {
				t.Errorf("GetUnparsedValue() gotV = %v, want %v", gotV, tt.wantV)
			}
		})
	}
}

func TestGetUnparsedFlagBoolean(t *testing.T) {
	tests := []struct {
		args    []string
		flag    string
		wantV   bool
		wantS   bool
		wantErr bool
	}{
		{
			[]string{"--rm="},
			"--rm",
			false,
			true,
			true,
		},
		{
			[]string{"--rm"},
			"--rm",
			true,
			true,
			false,
		},
		{
			[]string{"--rm", "--other"},
			"--rm",
			true,
			true,
			false,
		},
		{
			[]string{"--rm", "-o"},
			"--rm",
			true,
			true,
			false,
		},
		{
			[]string{"--rm=value"},
			"--rm",
			false,
			true,
			true,
		},
		{
			[]string{"--rm=true"},
			"--rm",
			true,
			true,
			false,
		},
		{
			[]string{"--rm=true"},
			"--rm",
			true,
			true,
			false,
		},
		{
			[]string{"--rm=True"},
			"--rm",
			true,
			true,
			false,
		},
		{
			[]string{"--rm=1"},
			"--rm",
			true,
			true,
			false,
		},
		{
			[]string{"--rm=false"},
			"--rm",
			false,
			true,
			false,
		},
		{
			[]string{"--rm=False"},
			"--rm",
			false,
			true,
			false,
		},
		{
			[]string{"--rm=0"},
			"--rm",
			false,
			true,
			false,
		},
		{
			[]string{"--do"},
			"--rm",
			false,
			false,
			false,
		},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			gotV, gotS, err := GetUnparsedBoolean(tt.args, tt.flag)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetUnparsedBoolean() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotS != tt.wantS {
				t.Errorf("GetUnparsedBoolean() gotS = %v, want %v", gotS, tt.wantS)
			}
			if gotV != tt.wantV {
				t.Errorf("GetUnparsedBoolean() gotV = %v, want %v", gotV, tt.wantV)
			}
		})
	}
}
