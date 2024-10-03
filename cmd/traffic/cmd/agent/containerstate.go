package agent

type containerState struct {
	mountPoint string
	env        map[string]string
}

func (c containerState) MountPoint() string {
	return c.mountPoint
}

func (c containerState) Env() map[string]string {
	return c.env
}

// NewContainerState creates a ContainerState that provides the environment variables and the mount point for a container.
func NewContainerState(mountPoint string, env map[string]string) ContainerState {
	return &containerState{
		mountPoint: mountPoint,
		env:        env,
	}
}
