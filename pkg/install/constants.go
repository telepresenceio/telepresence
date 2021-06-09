package install

const (
	AgentContainerName        = "traffic-agent"
	AgentAnnotationVolumeName = "traffic-annotations"
	DomainPrefix              = "telepresence.getambassador.io/"
	ManagerAppName            = "traffic-manager"
	ManagerPortHTTP           = 8081
	TelAppMountPoint          = "/tel_app_mounts"
)
