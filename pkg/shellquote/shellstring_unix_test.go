//go:build !windows
// +build !windows

package shellquote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    []string
		wantErr bool
	}{
		{
			name:    "Empty",
			line:    "",
			want:    nil,
			wantErr: false,
		},
		{
			name:    "single quoted",
			line:    `'one quoted' 'two quoted'`,
			want:    []string{`one quoted`, `two quoted`},
			wantErr: false,
		},
		{
			name:    "escape in single quoted",
			line:    `'\one'`,
			want:    []string{`\one`},
			wantErr: false,
		},
		{
			name:    "escape in single quoted",
			line:    `'\'one'`, // unbalanced. There's no escape in single quote
			want:    nil,
			wantErr: true,
		},
		{
			name:    "single quoted concat",
			line:    `'one quoted''two quoted'`,
			want:    []string{`one quotedtwo quoted`},
			wantErr: false,
		},
		{
			name:    "double quoted",
			line:    `"one quoted" "two quoted"`,
			want:    []string{`one quoted`, `two quoted`},
			wantErr: false,
		},
		{
			name:    "double quoted concat",
			line:    `"one quoted""two quoted"`,
			want:    []string{`one quotedtwo quoted`},
			wantErr: false,
		},
		{
			name:    "double quoted with escaped quote",
			line:    `"one \"quoted\"" "two quoted"`,
			want:    []string{`one "quoted"`, `two quoted`},
			wantErr: false,
		},
		{
			name:    "double quoted with unbalanced escaped quote",
			line:    `"one \"quoted\" "two quoted"`,
			want:    nil,
			wantErr: true,
		},
		{
			name:    "double quoted with escaped dollar",
			line:    `"\$32.0"`,
			want:    []string{`$32.0`},
			wantErr: false,
		},
		{
			name:    "double quoted with escaped escape",
			line:    `"the \\ character"`,
			want:    []string{`the \ character`},
			wantErr: false,
		},
		{
			name:    "double quoted with escaped newline",
			line:    "\"the line \\\ncontinues here\"",
			want:    []string{`the line continues here`},
			wantErr: false,
		},
		{
			name:    "double quoted with escaped newline",
			line:    "\"the line \\\ncontinues here\"",
			want:    []string{`the line continues here`},
			wantErr: false,
		},
		{
			name:    "not quoted",
			line:    `not quoted`,
			want:    []string{`not`, `quoted`},
			wantErr: false,
		},
		{
			name:    "backslash escape",
			line:    `one\ two three\ four`,
			want:    []string{`one two`, `three four`},
			wantErr: false,
		},
		{
			name:    "double and singe quoted concat",
			line:    `"one quoted"'two quoted'`,
			want:    []string{`one quotedtwo quoted`},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Split(tt.line)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				got, err := Split(ShellArgsString(got))
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
