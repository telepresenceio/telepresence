package agentmap

import "testing"

func TestManagerHostUsesClusterDomain(t *testing.T) {
	got := ManagerHost("traffic", "cluster.local.")
	want := "traffic-manager.traffic.svc.cluster.local"
	if got != want {
		t.Fatalf("ManagerHost() = %q, want %q", got, want)
	}
}

func TestManagerHostKeepsLegacyHostWhenClusterDomainUnknown(t *testing.T) {
	got := ManagerHost("traffic", "")
	want := "traffic-manager.traffic"
	if got != want {
		t.Fatalf("ManagerHost() = %q, want %q", got, want)
	}
}
