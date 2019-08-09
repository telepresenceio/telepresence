package kubeapply_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/datawire/teleproxy/pkg/dtest"
	"github.com/datawire/teleproxy/pkg/kubeapply"
)

func TestDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip(err)
	}

	if os.Getenv("DOCKER_REGISTRY") == "" {
		os.Setenv("DOCKER_REGISTRY", dtest.DockerRegistry())
	}

	_, err := kubeapply.ExpandResource("docker.yaml")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
