package main

import (
	"os"
	"testing"

	"github.com/bmizerany/assert"
)

func TestWatchSet_Interpolate(t *testing.T) {
	_ = os.Setenv("HOST_IP", "172.10.0.1")
	_ = os.Setenv("ANOTHER_IP", "172.10.0.2")

	set := WatchSet{
		ConsulWatches: []ConsulWatchSpec{
			{ConsulAddress: "${HOST_IP}", ServiceName: "foo-in-consul", Datacenter: "dc1"},
			{ConsulAddress: "$ANOTHER_IP", ServiceName: "bar-in-consul", Datacenter: "dc1"},
			{ConsulAddress: "127.0.0.1", ServiceName: "baz-in-consul", Datacenter: "dc1"},
		},
	}

	interpolated := set.interpolate()
	assert.Equal(t,
		ConsulWatchSpec{ConsulAddress: "172.10.0.1", ServiceName: "foo-in-consul", Datacenter: "dc1"},
		interpolated.ConsulWatches[0])

	assert.Equal(t,
		ConsulWatchSpec{ConsulAddress: "172.10.0.2", ServiceName: "bar-in-consul", Datacenter: "dc1"},
		interpolated.ConsulWatches[1])

	assert.Equal(t,
		ConsulWatchSpec{ConsulAddress: "127.0.0.1", ServiceName: "baz-in-consul", Datacenter: "dc1"},
		interpolated.ConsulWatches[2])
}
