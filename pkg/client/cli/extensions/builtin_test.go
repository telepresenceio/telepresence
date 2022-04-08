package extensions_test

import (
	"testing"

	"github.com/blang/semver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/extensions"
)

func TestHTTPCompatible(t *testing.T) {
	t.Parallel()

	oldTests := []struct {
		name     string
		agentVer semver.Version
		args     []string
		want     []string
		wantErr  assert.ErrorAssertionFunc
	}{
		{
			"1.11.7 (a)",
			semver.MustParse("1.11.7"),
			[]string{"--match=auto", "--header=b=c", "--plaintext=false", "--meta=", "--path-prefix="},
			[]string{"--match=auto", "--match=b=c"},
			assert.NoError,
		},
		{
			"1.11.7 (b)",
			semver.MustParse("1.11.7"),
			[]string{"--header=b=c", "--plaintext=false", "--meta=", "--path-prefix="},
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
			[]string{"--match=auto", "--plaintext=true"},
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
			"1.11.8, one auto (a)",
			semver.MustParse("1.11.8"),
			[]string{"--match=auto", "--header=auto"},
			[]string{"--match=auto", "--match=auto", "--plaintext=false"},
			assert.NoError,
		},
		{
			"1.11.8, one auto (b)",
			semver.MustParse("1.11.8"),
			[]string{},
			[]string{"--match=auto", "--plaintext=false"},
			assert.NoError,
		},
		{
			"1.11.9",
			semver.MustParse("1.11.9"),
			[]string{"--match=x=y", "--path-prefix=/api", "--meta=a=b"},
			[]string{"--header=x=y", "--meta=a=b", "--path-equal=", "--path-prefix=/api", "--path-regex=", "--plaintext=false"},
			assert.NoError,
		},
		{
			"1.11.9, strip match=auto (a)",
			semver.MustParse("1.11.9"),
			[]string{"--header=a=b"},
			[]string{"--header=a=b", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"},
			assert.NoError,
		},
		{
			"1.11.9, strip match=auto (b)",
			semver.MustParse("1.11.9"),
			[]string{"--match=auto", "--header=a=b"},
			[]string{"--header=auto", "--header=a=b", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"},
			assert.NoError,
		},
		{
			"no agent version, one auto (a)",
			semver.Version{},
			[]string{},
			[]string{"--header=auto", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"},
			assert.NoError,
		},
		{
			"no agent version, one auto (b)",
			semver.Version{},
			[]string{"--match=auto", "--header=auto"},
			[]string{"--header=auto", "--header=auto", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"},
			assert.NoError,
		},
		{
			"no agent version, strip header=auto (a)",
			semver.Version{},
			[]string{"--match=a=b"},
			[]string{"--header=a=b", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"},
			assert.NoError,
		},
		{
			"no agent version, strip header=auto (b)",
			semver.Version{},
			[]string{"--header=auto", "--match=a=b"},
			[]string{"--header=a=b", "--header=auto", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"},
			assert.NoError,
		},
		{
			"no agent version, strip match=auto",
			semver.Version{},
			[]string{"--match=auto", "--header=a=b"},
			[]string{"--header=auto", "--header=a=b", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"},
			assert.NoError,
		},
	}

	type TestCase struct {
		InputImage string
		InputArgs  []string
		OutputArgs []string
		OutputErr  assert.ErrorAssertionFunc
	}
	exactErr := func(str string) assert.ErrorAssertionFunc {
		return func(t assert.TestingT, err error, msgAndArgs ...interface{}) bool {
			return assert.EqualError(t, err, str, msgAndArgs...)
		}
	}
	testcases := map[string]TestCase{
		"invalid-positional": {"", []string{"pos"}, nil, exactErr(`unexpected positional arguments: 1: ["pos"]`)},
		"invalid-name":       {"", []string{"--bogus"}, nil, exactErr("unknown flag: --bogus")},
		"nil":                {"", nil, []string{"--header=auto", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"}, assert.NoError},
		"empty":              {"", []string{}, []string{"--header=auto", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"}, assert.NoError},
		"empty-1.11.8":       {"reg.tld/tel2:1.11.8", []string{}, []string{"--match=auto", "--plaintext=false"}, assert.NoError},
		"empty-1.11.7":       {"reg.tld/tel2:1.11.7", []string{}, []string{"--match=auto"}, assert.NoError},
		"header":             {"", []string{"--header=foo=bar"}, []string{"--header=foo=bar", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"}, assert.NoError},
		"match":              {"", []string{"--match=foo=bar"}, []string{"--header=foo=bar", "--path-equal=", "--path-prefix=", "--path-regex=", "--plaintext=false"}, assert.NoError},
	}
	for _, oldTest := range oldTests {
		oldTest := oldTest
		newTest := TestCase{
			InputImage: "reg.tld/tel2:" + oldTest.agentVer.String(),
			InputArgs:  oldTest.args,
			OutputArgs: oldTest.want,
			OutputErr:  oldTest.wantErr,
		}
		if oldTest.agentVer.String() == "0.0.0" {
			newTest.InputImage = ""
		}
		name := "old-" + oldTest.name
		if _, exists := testcases[name]; exists {
			t.Fatalf("duplicate test name: %q", name)
		}
		testcases[name] = newTest
	}

	for tcName, tc := range testcases {
		tc := tc
		t.Run(tcName, func(t *testing.T) {
			t.Parallel()
			t.Logf("image=%q", tc.InputImage)
			ctx := dlog.NewTestContext(t, true)
			env, err := client.LoadEnv(ctx)
			require.NoError(t, err)
			ctx = client.WithEnv(ctx, env)
			cfg, err := client.LoadConfig(ctx)
			require.NoError(t, err)
			ctx = client.WithConfig(ctx, cfg)

			outArgs, outErr := extensions.MakeArgsCompatible(ctx, "http", tc.InputImage, tc.InputArgs)

			tc.OutputErr(t, outErr)
			assert.Equal(t, tc.OutputArgs, outArgs)
		})
	}
}
