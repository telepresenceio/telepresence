package env

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/dotenv"
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
			`csh A=B C`,
			SyntaxCsh,
			`A`,
			`B C`,
			`set A='B C'`,
		},
		{
			`csh A=B!C`,
			SyntaxCsh,
			`A`,
			`B!C`,
			`set A=B\!C`,
		},
		{
			`csh:export A=B\nC!`,
			SyntaxCshExport,
			`A`,
			"B\nC!",
			"setenv A 'B\\\nC\\!'",
		},
		{
			`csh:export A=B 'C X'`,
			SyntaxCshExport,
			`A`,
			`B 'C X'`,
			`setenv A 'B '\''C X'\'`,
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

func TestSyntax_WriteEntry_composeRoundTrip(t *testing.T) {
	values := []string{
		``,
		`plain`,
		`B C`,
		`B 'C X'`,
		`it's`,
		`C:\dir\`,
		`C:\dir\ x`,
		`a\'b`,
		`a\\'b`,
		`\`,
		`\n is not a newline`,
		`$HOME and ${PATH}`,
		`$$`,
		`"double"`,
		"B\nC\t\"D\"",
		"multi\nline $HOME\nC:\\dir\\",
		"line\\nbreak\r\n",
		`# not a comment`,
		`value #comment`,
	}
	for _, v := range values {
		t.Run(v, func(t *testing.T) {
			r, err := SyntaxCompose.WriteEntry("A", v)
			require.NoError(t, err)
			env, err := dotenv.UnmarshalWithLookup(r, func(string) (string, bool) { return "x", true })
			require.NoError(t, err, r)
			require.Equal(t, v, env["A"], r)
		})
	}
}

func TestSyntax_WriteEntry_cshRoundTrip(t *testing.T) {
	values := []string{
		``,
		`plain`,
		`B C`,
		`it's`,
		`!`,
		`B!C`,
		`!!`,
		`a\!b`,
		`$HOME and ${PATH}`,
		"`date`",
		`*?[a]~{b}`,
		`\`,
		`C:\dir\`,
		"multi\nline",
		"two\n\nnewlines\n",
		"backslash\\\nnewline",
		"tab\tcr\r",
		"^x^y",
	}
	shells := 0
	for _, sh := range []string{"tcsh", "csh"} {
		shPath, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		shells++
		for _, e := range []Syntax{SyntaxCsh, SyntaxCshExport} {
			for _, v := range values {
				t.Run(sh+"/"+syntaxNames[e]+"/"+v, func(t *testing.T) {
					r, err := e.WriteEntry("A", v)
					require.NoError(t, err)
					dir := t.TempDir()
					require.NoError(t, os.WriteFile(filepath.Join(dir, "env.csh"), []byte(r+"\n"), 0o600))
					script := "source env.csh\n"
					if e == SyntaxCsh {
						script += "setenv A $A:q\n"
					}
					script += "printenv A\n"
					require.NoError(t, os.WriteFile(filepath.Join(dir, "run.csh"), []byte(script), 0o600))
					cmd := exec.Command(shPath, "-f", "run.csh")
					cmd.Dir = dir
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s: %s", r, out)
					require.Equal(t, v+"\n", string(out), r)
				})
			}
		}
	}
	if shells == 0 {
		t.Skip("neither tcsh nor csh is installed")
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
