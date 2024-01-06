package daemon

import (
	"reflect"
	"testing"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
)

func Test_parseSubnetViaWorkload(t *testing.T) {
	tests := []struct {
		name    string
		dps     string
		want    *daemon.SubnetViaWorkload
		wantErr bool
	}{
		{
			"empty",
			"",
			nil,
			true,
		},
		{
			"workload with dot",
			"127.1.2.3/32=workload.namespace",
			nil,
			true,
		},
		{
			"invalid subnet",
			"bad=workload",
			nil,
			true,
		},
		{
			"ok",
			"127.1.2.3/32=workload",
			&daemon.SubnetViaWorkload{
				Subnet:   "127.1.2.3/32",
				Workload: "workload",
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSubnetViaWorkload(tt.dps)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDomainProxy(%q) error = %v, wantErr %v", tt.dps, err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDomainProxy(%q) got = %v, want %v", tt.dps, got, tt.want)
			}
		})
	}
}
