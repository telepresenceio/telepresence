package annotation

import (
	"context"

	"github.com/datawire/dlib/dlog"
)

const (
	DomainPrefix = "telepresence.io/"

	Config                   = DomainPrefix + "agent-config"
	InjectContainerPorts     = DomainPrefix + "inject-container-ports"
	InjectIgnoreVolumeMounts = DomainPrefix + "inject-ignore-volume-mounts"
	InjectServiceName        = DomainPrefix + "inject-service-name"
	InjectServicePorts       = DomainPrefix + "inject-service-ports"
	InjectTrafficAgent       = DomainPrefix + "inject-traffic-agent"
	ManuallyInjected         = DomainPrefix + "manually-injected"
	ReplacedContainerPrefix  = DomainPrefix + "replaced-container."
	RestartedAt              = DomainPrefix + "restartedAt"
	VolumeMountPolicies      = DomainPrefix + "mount-policies"

	LegacyDomainPrefix             = "telepresence.getambassador.io/"
	LegacyInjectContainerPorts     = LegacyDomainPrefix + "inject-container-ports"
	LegacyInjectIgnoreVolumeMounts = LegacyDomainPrefix + "inject-ignore-volume-mounts"
	LegacyInjectServiceName        = LegacyDomainPrefix + "inject-service-name"
	LegacyInjectServicePort        = LegacyDomainPrefix + "inject-service-port"
	LegacyInjectTrafficAgent       = LegacyDomainPrefix + "inject-traffic-agent"
	LegacyManuallyInjected         = LegacyDomainPrefix + "manually-injected"
)

func GetAnnotation(ctx context.Context, annotations map[string]string, key, deprecatedKey string) string {
	value, ok := annotations[key]
	if !ok {
		value, ok = annotations[deprecatedKey]
		if ok {
			dlog.Warningf(ctx, "Annotation %q is deprecated. Use %q instead", key, value)
		}
	}
	return value
}

func ReplaceAnnotationKey(cn string) string {
	return ReplacedContainerPrefix + cn
}
