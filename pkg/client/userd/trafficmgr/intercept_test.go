package trafficmgr

import (
	"fmt"
	"testing"

	"github.com/blang/semver"
	"github.com/stretchr/testify/assert"
)

func Test_makeFlagsCompatible(t *testing.T) {
	tests := []struct {
		name     string
		agentVer semver.Version
		args     []string
		want     []string
		wantErr  assert.ErrorAssertionFunc
	}{
		{
			"1.11.7",
			semver.MustParse("1.11.7"),
			[]string{"--match=a=b", "--header=b=c", "--plaintext=false", "--meta=", "--path-prefix="},
			[]string{"--match=a=b", "--match=b=c"},
			assert.NoError,
		},
		{
			"1.11.7-plaintext",
			semver.MustParse("1.11.7"),
			[]string{"--plaintext=true"},
			nil,
			assert.Error,
		},
		{
			"1.11.8",
			semver.MustParse("1.11.8"),
			[]string{"--plaintext=true"},
			[]string{"--plaintext=true"},
			assert.NoError,
		},
		{
			"1.11.8-meta",
			semver.MustParse("1.11.8"),
			[]string{"--meta=a=b"},
			nil,
			assert.Error,
		},
		{
			"1.11.9",
			semver.MustParse("1.11.9"),
			[]string{"--path-prefix=/api", "--meta=a=b"},
			[]string{"--meta=a=b", "--path-prefix=/api"},
			assert.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := makeFlagsCompatible(&tt.agentVer, tt.args)
			if !tt.wantErr(t, err, fmt.Sprintf("makeFlagsCompatible(%v, %v)", tt.agentVer, tt.args)) {
				return
			}
			assert.Equalf(t, tt.want, got, "makeFlagsCompatible(%v, %v)", tt.agentVer, tt.args)
		})
	}
}
