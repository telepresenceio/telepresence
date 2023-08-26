package daemon_test

import (
	"context"
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

func TestDaemonInfoFileName(t *testing.T) {
	tests := map[struct {
		name string
		port int
	}]string{
		{name: "the-cure", port: 8080}:                                            "the-cure-8080.json",
		{name: "arn:aws:eks:us-east-2:914373874199:cluster/test-auth", port: 443}: "arn-aws-eks-us-east-2-914373874199-cluster-test-auth-443.json",
		{name: "gke_datawireio_us-central1-b_kube-staging-apps-1", port: 80}:      "gke_datawireio_us-central1-b_kube-staging-apps-1-80.json",
	}
	for test, expected := range tests {
		result := daemon.InfoFile(test.name, test.port)
		if result != expected {
			t.Fatalf("InfoFile gave bad output; expected %s got %s", expected, result)
		}
	}
}

func TestDaemonInfoFilePort(t *testing.T) {
	tests := map[struct {
		name string
		port int
	}]int{
		{name: "the-cure", port: 8080}:                                            8080,
		{name: "arn:aws:eks:us-east-2:914373874199:cluster/test-auth", port: 443}: 443,
		{name: "gke_datawireio_us-central1-b_kube-staging-apps-1", port: 80}:      80,
	}
	ctx := context.Background()
	for test, expected := range tests {
		if err := daemon.SaveInfo(ctx, &daemon.Info{}, daemon.InfoFile(test.name, test.port)); err != nil {
			t.Fatal(err)
		}
		port, err := daemon.PortForName(ctx, test.name)
		if err != nil {
			t.Fatal(err)
		}
		if port != expected {
			t.Fatalf("bad port; expected %d found %d", expected, port)
		}
	}
}
