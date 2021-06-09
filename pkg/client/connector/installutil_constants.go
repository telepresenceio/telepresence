package connector

const (
	agentContainerName        = "traffic-agent"
	agentAnnotationVolumeName = "traffic-annotations"
	domainPrefix              = "telepresence.getambassador.io/"
	managerAppName            = "traffic-manager"
	ManagerPortHTTP           = 8081
	telAppMountPoint          = "/tel_app_mounts"
)
