package cache_test

import (
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

func TestDaemonInfoFileName(t *testing.T) {
	tests := map[struct {
		name string
		port int
	}]string{
		{name: "the-cure", port: 8080}:                                            "the-cure-8080.json",
		{name: "arn:aws:eks:us-east-2:914373874199:cluster/test-auth", port: 443}: "arn-aws-eks-us-east-2-914373874199-cluster-test-auth-443.json",
	}
	for test, expected := range tests {
		result := cache.DaemonInfoFile(test.name, test.port)
		if result != expected {
			t.Fatalf("DaemonInfoFile gave bad output; expected %s got %s", expected, result)
		}
	}
}
