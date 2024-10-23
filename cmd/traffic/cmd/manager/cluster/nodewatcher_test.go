package cluster

import (
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

var (
	oneCIDR   = netip.MustParsePrefix("192.168.0.0/24")
	twoCIDR   = netip.MustParsePrefix("192.168.1.0/24")
	threeCIDR = netip.MustParsePrefix("192.168.2.0/24")
	fourCIDR  = netip.MustParsePrefix("192.168.3.0/24")
	fiveCIDR  = netip.MustParsePrefix("192.168.4.0/24")
)

func Test_getNodeDelta(t *testing.T) {
	type args struct {
		oldSubnets []netip.Prefix
		newSubnets []netip.Prefix
	}
	tests := []struct {
		name        string
		args        args
		wantAdded   []netip.Prefix
		wantDropped []netip.Prefix
	}{
		{
			name: "nil",
			args: args{
				oldSubnets: nil,
				newSubnets: nil,
			},
			wantDropped: nil,
			wantAdded:   nil,
		},
		{
			name: "just new",
			args: args{
				oldSubnets: nil,
				newSubnets: []netip.Prefix{oneCIDR, twoCIDR, threeCIDR},
			},
			wantDropped: nil,
			wantAdded:   []netip.Prefix{oneCIDR, twoCIDR, threeCIDR},
		},
		{
			name: "just old",
			args: args{
				oldSubnets: []netip.Prefix{oneCIDR, twoCIDR, threeCIDR},
				newSubnets: nil,
			},
			wantDropped: []netip.Prefix{oneCIDR, twoCIDR, threeCIDR},
			wantAdded:   nil,
		},
		{
			name: "same old and new",
			args: args{
				oldSubnets: []netip.Prefix{oneCIDR, twoCIDR, threeCIDR},
				newSubnets: []netip.Prefix{oneCIDR, twoCIDR, threeCIDR},
			},
			wantDropped: nil,
			wantAdded:   nil,
		},
		{
			name: "more old than new",
			args: args{
				oldSubnets: []netip.Prefix{oneCIDR, twoCIDR, threeCIDR, fourCIDR, fiveCIDR},
				newSubnets: []netip.Prefix{twoCIDR, threeCIDR, fiveCIDR},
			},
			wantDropped: []netip.Prefix{oneCIDR, fourCIDR},
			wantAdded:   nil,
		},
		{
			name: "less old than new",
			args: args{
				oldSubnets: []netip.Prefix{oneCIDR, threeCIDR, fourCIDR},
				newSubnets: []netip.Prefix{oneCIDR, twoCIDR, threeCIDR, fourCIDR, fiveCIDR},
			},
			wantDropped: nil,
			wantAdded:   []netip.Prefix{twoCIDR, fiveCIDR},
		},
		{
			name: "different old than new",
			args: args{
				oldSubnets: []netip.Prefix{oneCIDR, threeCIDR, fourCIDR},
				newSubnets: []netip.Prefix{twoCIDR, threeCIDR, fiveCIDR},
			},
			wantDropped: []netip.Prefix{oneCIDR, fourCIDR},
			wantAdded:   []netip.Prefix{twoCIDR, fiveCIDR},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAdded, gotDropped := getSubnetsDelta(tt.args.oldSubnets, tt.args.newSubnets)
			if !reflect.DeepEqual(gotAdded, tt.wantAdded) {
				t.Errorf("getDelta() gotAdded = %v, want %v", gotAdded, tt.wantAdded)
			}
			if !reflect.DeepEqual(gotDropped, tt.wantDropped) {
				t.Errorf("getDelta() gotDropped = %v, want %v", gotDropped, tt.wantDropped)
			}
		})
	}
}

func Test_nodeSubnets(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want []netip.Prefix
	}{
		{
			name: "nil node",
			node: nil,
			want: nil,
		},
		{
			name: "node with no podCIDR",
			node: &corev1.Node{},
			want: nil,
		},
		{
			name: "node with podCIDRs",
			node: &corev1.Node{
				Spec: corev1.NodeSpec{
					PodCIDR: "192.168.0.0/24",
				},
			},
			want: []netip.Prefix{oneCIDR},
		},
		{
			name: "node with podCIDR and podCIDRs",
			node: &corev1.Node{
				Spec: corev1.NodeSpec{
					PodCIDRs: []string{"192.168.0.0/24", "192.168.1.0/24"},
				},
			},
			want: []netip.Prefix{oneCIDR, twoCIDR},
		},
		{
			name: "pod with podIP and podSubnets",
			node: &corev1.Node{
				Spec: corev1.NodeSpec{
					PodCIDR:  "192.168.0.0/24",
					PodCIDRs: []string{"192.168.0.0/24", "192.168.1.0/24"},
				},
			},
			want: []netip.Prefix{oneCIDR, twoCIDR},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeSubnets(dlog.NewTestContext(t, false), tt.node); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("nodeSubnets() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_nodeWatcher_add(t *testing.T) {
	w := &nodeWatcher{subnets: subnet.Set{}}
	w.add([]netip.Prefix{oneCIDR, twoCIDR})
	assert.Equal(t, 2, len(w.subnets))
	assert.False(t, w.changed.IsZero(), "Changed time not se when adding subnet")

	// reset changed and add an existing subnet
	w.changed = time.Time{}
	w.add([]netip.Prefix{twoCIDR})
	assert.Equal(t, 2, len(w.subnets))
	assert.True(t, w.changed.IsZero(), "Adding existing subnet caused changed time to be set")

	w.add([]netip.Prefix{oneCIDR, threeCIDR})
	assert.False(t, w.changed.IsZero(), "Changed time not se when adding subnet")
	assert.Equal(t, 3, len(w.subnets))
}
