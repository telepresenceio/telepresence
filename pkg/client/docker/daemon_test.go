package docker

import (
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func TestSelectControlPlane(t *testing.T) {
	// Helpers that build a candidate for a container with a given id/labels/image at a given address.
	cand := func(id, addr string, image string, labels map[string]string) cpCandidate {
		return cpCandidate{
			container: &container.InspectResponse{
				ID:   id,
				Name: "/" + id,
				Config: &container.Config{
					Image:  image,
					Labels: labels,
				},
			},
			networkName: "net",
			localAddr:   netip.MustParseAddrPort(addr),
		}
	}
	kindCP := func(id, addr string) cpCandidate {
		return cand(id, addr, "kindest/node:v1.35.0", map[string]string{"io.x-k8s.kind.role": "control-plane"})
	}
	minikubeNode := func(id, addr, profile string) cpCandidate {
		return cand(id, addr, "gcr.io/k8s-minikube/kicbase:v0.0.42", map[string]string{"name.minikube.sigs.k8s.io": profile})
	}
	k3sServer := func(id, addr string) cpCandidate {
		return cand(id, addr, "docker.io/rancher/k3s:v1.31.0-k3s1", nil)
	}
	k3dProxy := func(id, addr string) cpCandidate {
		return cand(id, addr, "ghcr.io/k3d-io/k3d-proxy:5.7.4", nil)
	}
	plain := func(id, addr string) cpCandidate {
		return cand(id, addr, "busybox", nil)
	}

	tests := []struct {
		name       string
		candidates []cpCandidate
		ncnID      string
		wantAddr   string // empty means "no selection"
	}{
		{
			name:       "single candidate is returned regardless of type",
			candidates: []cpCandidate{plain("c1", "10.0.0.1:6443")},
			ncnID:      "c1",
			wantAddr:   "10.0.0.1:6443",
		},
		{
			name: "multiple kind clusters share the network: publisher wins",
			candidates: []cpCandidate{
				kindCP("dev", "172.18.0.3:6443"),
				kindCP("other-a", "172.18.0.5:6443"),
				kindCP("other-b", "172.18.0.6:6443"),
			},
			ncnID:    "dev",
			wantAddr: "172.18.0.3:6443", // not an arbitrary other cluster
		},
		{
			name: "minikube multinode: publisher (control-plane) wins over workers",
			candidates: []cpCandidate{
				minikubeNode("m02", "192.168.49.3:8443", "demo"),
				minikubeNode("minikube", "192.168.49.2:8443", "demo"),
				minikubeNode("m03", "192.168.49.4:8443", "demo"),
			},
			ncnID:    "minikube",
			wantAddr: "192.168.49.2:8443",
		},
		{
			name: "k3d: publisher is the load-balancer, fall back to the k3s server",
			candidates: []cpCandidate{
				k3dProxy("serverlb", "172.20.0.2:6443"),
				k3sServer("server-0", "172.20.0.3:6443"),
			},
			ncnID:    "serverlb", // the proxy publishes the port but isn't a recognized node
			wantAddr: "172.20.0.3:6443",
		},
		{
			name: "k3s server publishes the port directly: publisher wins",
			candidates: []cpCandidate{
				plain("sidecar", "172.20.0.9:6443"),
				k3sServer("server-0", "172.20.0.3:6443"),
			},
			ncnID:    "server-0",
			wantAddr: "172.20.0.3:6443",
		},
		{
			name: "publisher is unrecognized and no candidate matches: no selection",
			candidates: []cpCandidate{
				plain("a", "10.0.0.1:6443"),
				plain("b", "10.0.0.2:6443"),
			},
			ncnID:    "a",
			wantAddr: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := selectControlPlane(tt.candidates, tt.ncnID)
			if tt.wantAddr == "" {
				if ok {
					t.Fatalf("expected no selection, got %s", got.localAddr)
				}
				return
			}
			if !ok {
				t.Fatalf("expected selection %s, got none", tt.wantAddr)
			}
			if got.localAddr.String() != tt.wantAddr {
				t.Errorf("selectControlPlane() = %s, want %s", got.localAddr, tt.wantAddr)
			}
		})
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
