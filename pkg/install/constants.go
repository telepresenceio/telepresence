package install

const (
	AgentContainerName        = "traffic-agent"
	InitContainerName         = "tel-agent-init"
	AgentAnnotationVolumeName = "traffic-annotations"
	AgentInjectorName         = "agent-injector"
	DomainPrefix              = "telepresence.getambassador.io/"
	InjectAnnotation          = DomainPrefix + "inject-" + AgentContainerName
	ServiceNameAnnotation     = DomainPrefix + "inject-service-name"
	ManualInjectAnnotation    = DomainPrefix + "manually-injected"
	ManagerAppName            = "traffic-manager"
	MutatorWebhookTLSName     = "mutator-webhook-tls"
)
