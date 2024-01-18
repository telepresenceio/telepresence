package daemon_test

import (
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func TestDaemonInfoFileName(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		result    string
	}{
		{name: "the-cure", namespace: "ns1", result: "the-cure-ns1.json"},
		{name: "arn:aws:eks:us-east-2:914373874199:cluster/test-auth", namespace: "ns1", result: "arn_aws_eks_us-east-2_914373874199_cluster_test-auth-ns1.json"},
		{name: "gke_datawireio_us-central1-b_kube-staging-apps-1", namespace: "ns1", result: "gke_datawireio_us-central1-b_kube-staging-apps-1-ns1.json"},
	}
	for _, test := range tests {
		di, err := daemon.NewIdentifier("", test.name, test.namespace, false)
		if err != nil {
			t.Fatal(err)
		}
		result := di.InfoFileName()
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
			if got := ioutil.SafeName(tt.name); got != tt.want {
				t.Errorf("SafeName() = %v, want %v", got, tt.want)
			}
		})
	}
}
