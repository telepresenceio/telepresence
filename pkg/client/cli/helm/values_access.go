package helm

// AuthModeEnforcing is the security.authentication.mode value under which the
// traffic-manager rejects callers it cannot authenticate.
const AuthModeEnforcing = "enforcing"

// DefaultInjectorName is the chart's default agentInjector.name.
const DefaultInjectorName = "agent-injector"

// AuthEnforced reports whether values select enforcing mode for
// security.authentication.mode. An unset mode is enforcing, as it is for the
// chart and the traffic-manager.
func (v *Values) AuthEnforced() bool {
	mode := v.Security.Authentication.Mode
	return mode == nil || *mode == "" || *mode == AuthModeEnforcing
}

// InjectorName returns the agent-injector Service name the values select, the
// chart's default when unset.
func (v *Values) InjectorName() string {
	if name := v.AgentInjector.Name; name != nil && *name != "" {
		return *name
	}
	return DefaultInjectorName
}
