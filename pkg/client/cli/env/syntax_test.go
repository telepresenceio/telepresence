package env

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyntax_WriteEntry(t *testing.T) {
	tests := []struct {
		name  string
		e     Syntax
		key   string
		value string
		want  string
	}{
		{
			`sh A=B C`,
			SyntaxSh,
			`A`,
			`B C`,
			`A='B C'`,
		},
		{
			`sh A=B "C"`,
			SyntaxSh,
			`A`,
			`B "C"`,
			`A='B "C"'`,
		},
		{
			`sh A="B C"`,
			SyntaxSh,
			`A`,
			`"B C"`,
			`A='"B C"'`,
		},
		{
			`sh A=B 'C X'`,
			SyntaxSh,
			`A`,
			`B 'C X'`,
			`A='B '\''C X'\'`,
		},
		{
			`compose A=B 'C X'`,
			SyntaxCompose,
			`A`,
			`B 'C X'`,
			`A='B \'C X\''`,
		},
		{
			`compose A=B\nC\t"D"`,
			SyntaxCompose,
			`A`,
			"B\nC\t\"D\"",
			`A="B\nC\t\"D\""`,
		},
		{
			`sh A='B C'`,
			SyntaxSh,
			`A`,
			`'B C'`,
			`A=\''B C'\'`,
		},
		{
			`sh A=\"B\" \"C\"`,
			SyntaxSh,
			`A`,
			`\"B\" \"C\"`,
			`A='\"B\" \"C\"'`,
		},
		{
			`ps A=B C`,
			SyntaxPS,
			`A`,
			`B C`,
			`$Env:A='B C'`,
		},
		{
			`ps A='B C'`,
			SyntaxPS,
			`A`,
			`'B C'`,
			`$Env:A='''B C'''`,
		},
		{
			`ps:export A='B C'`,
			SyntaxPSExport,
			`A`,
			`'B C'`,
			`[Environment]::SetEnvironmentVariable('A', '''B C''', 'User')`,
		},
		{
			`ps:export A=B C`,
			SyntaxPSExport,
			`A`,
			`B C`,
			`[Environment]::SetEnvironmentVariable('A', 'B C', 'User')`,
		},
		{
			`ps:export A="B C"`,
			SyntaxPSExport,
			`A`,
			`"B C"`,
			`[Environment]::SetEnvironmentVariable('A', '"B C"', 'User')`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := tt.e.WriteEntry(tt.key, tt.value)
			require.NoError(t, err)
			require.Equal(t, tt.want, r)
		})
	}
}

func TestFlags_MaybeWrite_stdout(t *testing.T) {
	saveStdout := os.Stdout
	t.Cleanup(func() { os.Stdout = saveStdout })
	stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stdout.Close() })
	os.Stdout = stdout

	f := Flags{File: "-", Syntax: SyntaxSh, JSON: "-"}
	require.NoError(t, f.MaybeWrite(map[string]string{"A": "B C"}))
	_, err = stdout.WriteString("after\n")
	require.NoError(t, err)
	require.NoError(t, stdout.Close())

	data, err := os.ReadFile(stdout.Name())
	require.NoError(t, err)
	require.Equal(t, "A='B C'\n{\n  \"A\": \"B C\"\n}\nafter\n", string(data))
}

func TestSyntax_WriteToFileAndClose(t *testing.T) {
	for _, e := range []Syntax{SyntaxDocker, SyntaxJSON} {
		t.Run(syntaxNames[e], func(t *testing.T) {
			file, err := os.Create(filepath.Join(t.TempDir(), "env"))
			require.NoError(t, err)
			require.NoError(t, e.WriteToFileAndClose(file, map[string]string{"A": "B"}))
			require.ErrorIs(t, file.Close(), os.ErrClosed)
		})
	}
}
