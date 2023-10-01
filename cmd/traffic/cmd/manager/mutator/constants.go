package mutator

import "github.com/telepresenceio/telepresence/v2/pkg/agentconfig"

const (
	DomainPrefix           = "telepresence.getambassador.io/"
	InjectAnnotation       = DomainPrefix + "inject-" + agentconfig.ContainerName
	ServiceNameAnnotation  = DomainPrefix + "inject-service-name"
	ManualInjectAnnotation = DomainPrefix + "manually-injected"
)
