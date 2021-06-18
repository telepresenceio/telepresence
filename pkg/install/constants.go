package install

const (
	AgentContainerName        = "traffic-agent"
	AgentAnnotationVolumeName = "traffic-annotations"
	AgentInjectorName         = "agent-injector"
	DomainPrefix              = "telepresence.getambassador.io/"
	InjectAnnotation          = DomainPrefix + "inject-" + AgentContainerName
	ServicePortAnnotation     = DomainPrefix + "inject-service-port"
	ManagerAppName            = "traffic-manager"
	ManagerPortHTTP           = 8081
	MutatorWebhookPortHTTPS   = 8443
	MutatorWebhookTLSName     = "mutator-webhook-tls"
	TelAppMountPoint          = "/tel_app_mounts"
)
