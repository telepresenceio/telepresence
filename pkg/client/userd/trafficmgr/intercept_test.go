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
			[]string{"--match=auto", "--header=b=c", "--plaintext=false", "--meta=", "--path-prefix="},
			[]string{"--match=b=c"},
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
			"1.11.8, one auto",
			semver.MustParse("1.11.8"),
			[]string{"--match=auto", "--header=auto"},
			[]string{"--match=auto"},
			assert.NoError,
		},
		{
			"1.11.9",
			semver.MustParse("1.11.9"),
			[]string{"--match=x=y", "--path-prefix=/api", "--meta=a=b"},
			[]string{"--header=x=y", "--meta=a=b", "--path-prefix=/api"},
			assert.NoError,
		},
		{
			"1.11.9, strip match=auto",
			semver.MustParse("1.11.9-rc.4"),
			[]string{"--match=auto", "--header=a=b"},
			[]string{"--header=a=b"},
			assert.NoError,
		},
		{
			"no agent version, one auto",
			semver.Version{},
			[]string{"--match=auto", "--header=auto"},
			[]string{"--header=auto"},
			assert.NoError,
		},
		{
			"no agent version, strip header=auto",
			semver.Version{},
			[]string{"--header=auto", "--match=a=b"},
			[]string{"--header=a=b"},
			assert.NoError,
		},
		{
			"no agent version, strip match=auto",
			semver.Version{},
			[]string{"--match=auto", "--header=a=b"},
			[]string{"--header=a=b"},
			assert.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ver := &tt.agentVer
			if ver.Major == 0 {
				ver = nil
			}
			got, err := makeFlagsCompatible(ver, tt.args)
			if !tt.wantErr(t, err, fmt.Sprintf("makeFlagsCompatible(%v, %v)", tt.agentVer, tt.args)) {
				return
			}
			assert.Equalf(t, tt.want, got, "makeFlagsCompatible(%v, %v)", tt.agentVer, tt.args)
		})
	}
}
