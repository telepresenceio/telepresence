package intercept

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvironmentSyntax_WriteEnv(t *testing.T) {
	tests := []struct {
		name  string
		e     EnvironmentSyntax
		key   string
		value string
		want  string
	}{
		{
			`sh A=B C`,
			envSyntaxSh,
			`A`,
			`B C`,
			`A='B C'`,
		},
		{
			`sh A=B "C"`,
			envSyntaxSh,
			`A`,
			`B "C"`,
			`A='B "C"'`,
		},
		{
			`sh A="B C"`,
			envSyntaxSh,
			`A`,
			`"B C"`,
			`A='"B C"'`,
		},
		{
			`sh A=B 'C X'`,
			envSyntaxSh,
			`A`,
			`B 'C X'`,
			`A='B '\''C X'\'`,
		},
		{
			`compose A=B 'C X'`,
			envSyntaxCompose,
			`A`,
			`B 'C X'`,
			`A='B \'C X\''`,
		},
		{
			`compose A=B\nC\t"D"`,
			envSyntaxCompose,
			`A`,
			"B\nC\t\"D\"",
			`A="B\nC\t\"D\""`,
		},
		{
			`sh A='B C'`,
			envSyntaxSh,
			`A`,
			`'B C'`,
			`A=\''B C'\'`,
		},
		{
			`sh A=\"B\" \"C\"`,
			envSyntaxSh,
			`A`,
			`\"B\" \"C\"`,
			`A='\"B\" \"C\"'`,
		},
		{
			`ps A=B C`,
			envSyntaxPS,
			`A`,
			`B C`,
			`$Env:A='B C'`,
		},
		{
			`ps A='B C'`,
			envSyntaxPS,
			`A`,
			`'B C'`,
			`$Env:A='''B C'''`,
		},
		{
			`ps:export A='B C'`,
			envSyntaxPSExport,
			`A`,
			`'B C'`,
			`[Environment]::SetEnvironmentVariable('A', '''B C''', 'User')`,
		},
		{
			`ps:export A=B C`,
			envSyntaxPSExport,
			`A`,
			`B C`,
			`[Environment]::SetEnvironmentVariable('A', 'B C', 'User')`,
		},
		{
			`ps:export A="B C"`,
			envSyntaxPSExport,
			`A`,
			`"B C"`,
			`[Environment]::SetEnvironmentVariable('A', '"B C"', 'User')`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := tt.e.WriteEnv(tt.key, tt.value)
			require.NoError(t, err)
			require.Equal(t, tt.want, r)
		})
	}
}
