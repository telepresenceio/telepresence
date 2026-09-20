package helm

import "testing"

func TestValues_AuthEnforced(t *testing.T) {
	tests := []struct {
		name string
		v    *Values
		want bool
	}{
		{"unset", &Values{}, false},
		{"permissive", &Values{Security: Security{Authentication: Authentication{Mode: new("permissive")}}}, false},
		{"enforcing", &Values{Security: Security{Authentication: Authentication{Mode: new(AuthModeEnforcing)}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.v.AuthEnforced(); got != tt.want {
				t.Errorf("AuthEnforced() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValues_InjectorName(t *testing.T) {
	tests := []struct {
		name string
		v    *Values
		want string
	}{
		{"unset", &Values{}, DefaultInjectorName},
		{"empty", &Values{AgentInjector: AgentInjector{Name: new("")}}, DefaultInjectorName},
		{"custom", &Values{AgentInjector: AgentInjector{Name: new("my-injector")}}, "my-injector"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.v.InjectorName(); got != tt.want {
				t.Errorf("InjectorName() = %q, want %q", got, tt.want)
			}
		})
	}
}
