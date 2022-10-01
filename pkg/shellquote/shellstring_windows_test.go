package shellquote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	// Tests inspired by https://docs.microsoft.com/en-us/previous-versions/ms880421(v=msdn.10)?redirectedfrom=MSDN
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
			name:    "double quoted with unbalanced escaped quote",
			line:    `"one \"quoted\" "two quoted"`,
			want:    nil,
			wantErr: true,
		},
		{
			name:    `"a b c" d e`,
			line:    `"a b c" d e`,
			want:    []string{`a b c`, `d`, `e`},
			wantErr: false,
		},
		{
			name:    `"ab\"c" "\\" d`,
			line:    `"ab\"c" "\\" d`,
			want:    []string{`ab"c`, `\`, `d`},
			wantErr: false,
		},
		{
			name:    `a\\\b d"e f"g h`,
			line:    `a\\\b d"e f"g h`,
			want:    []string{`a\\\b`, `de fg`, `h`},
			wantErr: false,
		},
		{
			name:    `a\\\"b c d`,
			line:    `a\\\"b c d`,
			want:    []string{`a\"b`, `c`, `d`},
			wantErr: false,
		},
		{
			name:    `a\\\\"b c" d e`,
			line:    `a\\\\"b c" d e`,
			want:    []string{`a\\b c`, `d`, `e`},
			wantErr: false,
		},
		{
			name:    `"a\\b c" d e`,
			line:    `"a\\b c" d e`,
			want:    []string{`a\\b c`, `d`, `e`},
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
				joined := ShellArgsString(got)
				got, err := Split(joined)
				require.NoError(t, err, joined)
				assert.Equal(t, tt.want, got, joined)
			}
		})
	}
}

func Test_quoteArg(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{
			name: `\`,
			arg:  `\`,
			want: `\`,
		},
		{
			name: `\ \`,
			arg:  `\ \`,
			want: `"\ \\"`,
		},
		{
			name: `\ \"`,
			arg:  `\ \"`,
			want: `"\ \\\""`,
		},
		{
			name: `\\ \\`,
			arg:  `\\ \\`,
			want: `"\\ \\\\"`,
		},
		{
			name: `\" \\`,
			arg:  `\" \\`,
			want: `"\\\" \\\\"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, []byte(tt.want), []byte(quoteArg(tt.arg)), "quoteArg(%v)", tt.arg)
		})
	}
}
