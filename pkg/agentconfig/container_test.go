package agentconfig

import (
	"testing"
)

func Test_prefixInterpolated(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{
			"empty",
			"",
			"",
		},
		{
			"empty_ipl",
			"$()",
			"$()",
		},
		{
			"alone",
			"$(IPL)",
			"$(_TEL_APP_A_IPL)",
		},
		{
			"normal",
			"Normal $(IPL) text",
			"Normal $(_TEL_APP_A_IPL) text",
		},
		{
			"escaped_ipl",
			"Escaped $$(IPL) text",
			"Escaped $$(IPL) text",
		},
		{
			"nested_ipl",
			"Nested $(IP$(IPL)) text",
			"Nested $(IP$(_TEL_APP_A_IPL)) text",
		},
		{
			"invalid_env",
			"Nested $(IP$) text",
			"Nested $(IP$) text",
		},
		{
			"unbalanced",
			"Unbalanced $(IPL text",
			"Unbalanced $(IPL text",
		},
		{
			"adjacent",
			"Adjacent $(IP1)$(IP2) text",
			"Adjacent $(_TEL_APP_A_IP1)$(_TEL_APP_A_IP2) text",
		},
		{
			"dollar-separated",
			"Dollar $(IP1)$$$(IP2) separated",
			"Dollar $(_TEL_APP_A_IP1)$$$(_TEL_APP_A_IP2) separated",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prefixInterpolated(tt.arg, "_TEL_APP_A_"); got != tt.want {
				t.Errorf("prefixInterpolated(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}
