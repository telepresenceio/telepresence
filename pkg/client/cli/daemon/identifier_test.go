package daemon_test

import (
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

func TestDaemonInfoFileName(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		port      int
		result    string
	}{
		{name: "the-cure", namespace: "ns1", port: 8080, result: "the-cure-ns1-8080.json"},
		{name: "arn:aws:eks:us-east-2:914373874199:cluster/test-auth", namespace: "ns1", port: 443, result: "arn_aws_eks_us-east-2_914373874199_cluster_test-auth-ns1-443.json"},
		{name: "gke_datawireio_us-central1-b_kube-staging-apps-1", namespace: "ns1", port: 80, result: "gke_datawireio_us-central1-b_kube-staging-apps-1-ns1-80.json"},
	}
	for _, test := range tests {
		result := daemon.NewIdentifier(test.name, test.namespace).DaemonInfoFileName(test.port)
		if result != test.result {
			t.Fatalf("DaemonInfoFile gave bad output; expected %s got %s", test.result, result)
		}
	}
}

func TestSafeContainerName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{
			"@",
			"a",
		},
		{
			"@x",
			"ax",
		},
		{
			"x@",
			"x_",
		},
		{
			"x@y",
			"x_y",
		},
		{
			"x™y", // multibyte char
			"x_y",
		},
		{
			"x™", // multibyte char
			"x_",
		},
		{
			"_y",
			"ay",
		},
		{
			"_y_",
			"ay_",
		},
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := daemon.SafeContainerName(tt.name); got != tt.want {
				t.Errorf("SafeContainerName() = %v, want %v", got, tt.want)
			}
		})
	}
}
