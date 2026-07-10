package agentconfig

import (
	"net/netip"
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestAgentGIDFromEnv(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		gid, ok, err := AgentGIDFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatalf("ok = true, want false")
		}
		if gid != 0 {
			t.Fatalf("gid = %d, want 0", gid)
		}
	})

	t.Run("empty", func(t *testing.T) {
		t.Setenv(EnvAgentGID, "")
		gid, ok, err := AgentGIDFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatalf("ok = true, want false")
		}
		if gid != 0 {
			t.Fatalf("gid = %d, want 0", gid)
		}
	})

	t.Run("valid", func(t *testing.T) {
		t.Setenv(EnvAgentGID, "7439")
		gid, ok, err := AgentGIDFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatalf("ok = false, want true")
		}
		if gid != 7439 {
			t.Fatalf("gid = %d, want 7439", gid)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Setenv(EnvAgentGID, "not-a-number")
		_, _, err := AgentGIDFromEnv()
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
	})
}

// sidecarWithTarget builds a single-container, single-intercept Sidecar config with the
// intercept's TargetPortNumeric set as requested, mirroring the fake-Config pattern used
// by cmd/traffic/cmd/agent's newPortHandler tests.
func sidecarWithTarget(numericTarget bool) *Sidecar {
	ic := &Intercept{
		ContainerPortName: "http",
		ServicePortName:   "http",
		ServicePort:       80,
		Protocol:          types.ProtoTCP,
		AgentPort:         9900,
		ContainerPort:     8080,
		TargetPortNumeric: numericTarget,
	}
	return &Sidecar{
		AgentName: "test",
		Containers: []*Container{{
			Name:       "app",
			Intercepts: []*Intercept{ic},
		}},
	}
}

// Test_PassThroughTarget guards the address a forwarder or protocol prober picks for
// the agent's pass-through dial to the application when no intercept is active: the pod
// IP's proxy port for a numeric target port, loopback for a named one. Dialing the pod
// IP for a named target port would hit the unconditional pod-IP redirect gate and loop
// back into the agent (see the doc comment on PassThroughTarget).
func Test_PassThroughTarget(t *testing.T) {
	ipv4 := netip.MustParseAddr("192.168.50.34")
	ipv6 := netip.MustParseAddr("fd00::34")

	t.Run("named target port dials IPv4 loopback", func(t *testing.T) {
		sc := sidecarWithTarget(false)
		got := sc.PassThroughTarget(ipv4, 8080, types.ProtoTCP)
		want := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 8080)
		if got != want {
			t.Fatalf("PassThroughTarget() = %v, want %v", got, want)
		}
	})

	t.Run("named target port dials IPv6 loopback", func(t *testing.T) {
		sc := sidecarWithTarget(false)
		got := sc.PassThroughTarget(ipv6, 8080, types.ProtoTCP)
		want := netip.AddrPortFrom(netip.IPv6Loopback(), 8080)
		if got != want {
			t.Fatalf("PassThroughTarget() = %v, want %v", got, want)
		}
	})

	t.Run("numeric target port dials the pod IP's proxy port", func(t *testing.T) {
		sc := sidecarWithTarget(true)
		got := sc.PassThroughTarget(ipv4, 8080, types.ProtoTCP)
		want := netip.AddrPortFrom(ipv4, sc.ProxyPort(sc.Containers[0].Intercepts[0].AgentPort))
		if got != want {
			t.Fatalf("PassThroughTarget() = %v, want %v", got, want)
		}
	})
}

func Test_LoopbackFor(t *testing.T) {
	if got := LoopbackFor(netip.MustParseAddr("10.1.2.3")); got != netip.AddrFrom4([4]byte{127, 0, 0, 1}) {
		t.Fatalf("LoopbackFor() = %v, want 127.0.0.1", got)
	}
	if got := LoopbackFor(netip.MustParseAddr("fd00::1")); got != netip.IPv6Loopback() {
		t.Fatalf("LoopbackFor() = %v, want ::1", got)
	}
}
