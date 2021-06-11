package install

const (
	AgentContainerName        = "traffic-agent"
	AgentAnnotationVolumeName = "traffic-annotations"
	DomainPrefix              = "telepresence.getambassador.io/"
	ManagerAppName            = "traffic-manager"
	ManagerPortHTTP           = 8081
	MutatorWebhookPortHTTPS   = 8443
	MutatorWebhookTLSName     = "mutator-webhook-tls"
	TelAppMountPoint          = "/tel_app_mounts"
)
