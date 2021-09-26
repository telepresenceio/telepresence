package cluster

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

var (
	oneIP   = iputil.IPKey(iputil.Parse("192.168.0.1"))
	nodeIP  = iputil.IPKey(iputil.Parse("192.168.0.2"))
	threeIP = iputil.IPKey(iputil.Parse("192.168.0.3"))
	fourIP  = iputil.IPKey(iputil.Parse("192.168.0.4"))
	fiveIP  = iputil.IPKey(iputil.Parse("192.168.0.5"))
)

func Test_getIPsDelta(t *testing.T) {
	type args struct {
		oldIPs []iputil.IPKey
		newIPs []iputil.IPKey
	}
	tests := []struct {
		name        string
		args        args
		wantAdded   []iputil.IPKey
		wantDropped []iputil.IPKey
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
				newIPs: []iputil.IPKey{oneIP, nodeIP, threeIP},
			},
			wantDropped: nil,
			wantAdded:   []iputil.IPKey{oneIP, nodeIP, threeIP},
		},
		{
			name: "just old",
			args: args{
				oldIPs: []iputil.IPKey{oneIP, nodeIP, threeIP},
				newIPs: nil,
			},
			wantDropped: []iputil.IPKey{oneIP, nodeIP, threeIP},
			wantAdded:   nil,
		},
		{
			name: "same old and new",
			args: args{
				oldIPs: []iputil.IPKey{oneIP, nodeIP, threeIP},
				newIPs: []iputil.IPKey{oneIP, nodeIP, threeIP},
			},
			wantDropped: nil,
			wantAdded:   nil,
		},
		{
			name: "more old than new",
			args: args{
				oldIPs: []iputil.IPKey{oneIP, nodeIP, threeIP, fourIP, fiveIP},
				newIPs: []iputil.IPKey{nodeIP, threeIP, fiveIP},
			},
			wantDropped: []iputil.IPKey{oneIP, fourIP},
			wantAdded:   nil,
		},
		{
			name: "less old than new",
			args: args{
				oldIPs: []iputil.IPKey{oneIP, threeIP, fourIP},
				newIPs: []iputil.IPKey{oneIP, nodeIP, threeIP, fourIP, fiveIP},
			},
			wantDropped: nil,
			wantAdded:   []iputil.IPKey{nodeIP, fiveIP},
		},
		{
			name: "different old than new",
			args: args{
				oldIPs: []iputil.IPKey{oneIP, threeIP, fourIP},
				newIPs: []iputil.IPKey{nodeIP, threeIP, fiveIP},
			},
			wantDropped: []iputil.IPKey{oneIP, fourIP},
			wantAdded:   []iputil.IPKey{nodeIP, fiveIP},
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

func Test_podIPKeys(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want []iputil.IPKey
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
			want: []iputil.IPKey{oneIP},
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
			want: []iputil.IPKey{oneIP, nodeIP},
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
			want: []iputil.IPKey{oneIP, nodeIP},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := podIPKeys(dlog.NewTestContext(t, false), tt.pod); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("podIPKeys() = %v, want %v", got, tt.want)
			}
		})
	}
}
