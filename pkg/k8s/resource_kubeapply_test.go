package k8s_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/datawire/teleproxy/pkg/dtest"
	"github.com/datawire/teleproxy/pkg/k8s"
)

func TestDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip(err)
	}

	if os.Getenv("DOCKER_REGISTRY") == "" {
		os.Setenv("DOCKER_REGISTRY", dtest.DockerRegistry())
	}

	_, err := k8s.ExpandResource("docker.yaml")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
