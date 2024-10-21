package cluster

import (
	"net/netip"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
)

var (
	oneIP   = netip.MustParseAddr("192.168.0.1")
	nodeIP  = netip.MustParseAddr("192.168.0.2")
	threeIP = netip.MustParseAddr("192.168.0.3")
	fourIP  = netip.MustParseAddr("192.168.0.4")
	fiveIP  = netip.MustParseAddr("192.168.0.5")
)

func Test_getIPsDelta(t *testing.T) {
	type args struct {
		oldIPs []netip.Addr
		newIPs []netip.Addr
	}
	tests := []struct {
		name        string
		args        args
		wantAdded   []netip.Addr
		wantDropped []netip.Addr
	}{
		{
			name: "nil",
			args: args{
				oldIPs: nil,
				newIPs: nil,
			},
			wantDropped: nil,
			wantAdded:   nil,
		},
		{
			name: "just new",
			args: args{
				oldIPs: nil,
				newIPs: []netip.Addr{oneIP, nodeIP, threeIP},
			},
			wantDropped: nil,
			wantAdded:   []netip.Addr{oneIP, nodeIP, threeIP},
		},
		{
			name: "just old",
			args: args{
				oldIPs: []netip.Addr{oneIP, nodeIP, threeIP},
				newIPs: nil,
			},
			wantDropped: []netip.Addr{oneIP, nodeIP, threeIP},
			wantAdded:   nil,
		},
		{
			name: "same old and new",
			args: args{
				oldIPs: []netip.Addr{oneIP, nodeIP, threeIP},
				newIPs: []netip.Addr{oneIP, nodeIP, threeIP},
			},
			wantDropped: nil,
			wantAdded:   nil,
		},
		{
			name: "more old than new",
			args: args{
				oldIPs: []netip.Addr{oneIP, nodeIP, threeIP, fourIP, fiveIP},
				newIPs: []netip.Addr{nodeIP, threeIP, fiveIP},
			},
			wantDropped: []netip.Addr{oneIP, fourIP},
			wantAdded:   nil,
		},
		{
			name: "less old than new",
			args: args{
				oldIPs: []netip.Addr{oneIP, threeIP, fourIP},
				newIPs: []netip.Addr{oneIP, nodeIP, threeIP, fourIP, fiveIP},
			},
			wantDropped: nil,
			wantAdded:   []netip.Addr{nodeIP, fiveIP},
		},
		{
			name: "different old than new",
			args: args{
				oldIPs: []netip.Addr{oneIP, threeIP, fourIP},
				newIPs: []netip.Addr{nodeIP, threeIP, fiveIP},
			},
			wantDropped: []netip.Addr{oneIP, fourIP},
			wantAdded:   []netip.Addr{nodeIP, fiveIP},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAdded, gotDropped := getIPsDelta(tt.args.oldIPs, tt.args.newIPs)
			if !reflect.DeepEqual(gotAdded, tt.wantAdded) {
				t.Errorf("getDelta() gotAdded = %v, want %v", gotAdded, tt.wantAdded)
			}
			if !reflect.DeepEqual(gotDropped, tt.wantDropped) {
				t.Errorf("getDelta() gotDropped = %v, want %v", gotDropped, tt.wantDropped)
			}
		})
	}
}

func Test_podIPs(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want []netip.Addr
	}{
		{
			name: "nil pod",
			pod:  nil,
			want: nil,
		},
		{
			name: "pod with no podIP",
			pod:  &corev1.Pod{},
			want: nil,
		},
		{
			name: "pod with podIP",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					PodIP: "192.168.0.1",
				},
			},
			want: []netip.Addr{oneIP},
		},
		{
			name: "pod with podIPs",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					PodIPs: []corev1.PodIP{
						{IP: "192.168.0.1"},
						{IP: "192.168.0.2"},
					},
				},
			},
			want: []netip.Addr{oneIP, nodeIP},
		},
		{
			name: "pod with podIP and podIPs",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					PodIP: "192.168.0.1", // should be first in PodIPs array
					PodIPs: []corev1.PodIP{
						{IP: "192.168.0.1"},
						{IP: "192.168.0.2"},
					},
				},
			},
			want: []netip.Addr{oneIP, nodeIP},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := podIPs(dlog.NewTestContext(t, false), tt.pod); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("podIPs() = %v, want %v", got, tt.want)
			}
		})
	}
}
