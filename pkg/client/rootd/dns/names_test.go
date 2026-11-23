package dns

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestValidName(t *testing.T) {
	good := []string{"cluster.local", "cluster.local.", "svc", "my-ns", "_http._tcp.svc", "a1.b2", "X.Y"}
	for _, n := range good {
		assert.True(t, ValidName(n, false), n)
	}
	assert.True(t, ValidName(".cluster.local", true))
	assert.False(t, ValidName(".cluster.local", false))
	assert.False(t, ValidName("..x", true))

	bad := []string{
		"", ".", "x/../../y", "../etc", "a/b", `a\b`, "a\nb", "a b", "a..b", "a.\nb", "/abs",
		"a:b", "a\x00b", strings.Repeat("a", 64) + ".x", strings.Repeat("a.", 127) + "a",
	}
	for _, n := range bad {
		assert.False(t, ValidName(n, true), n)
	}
	long := strings.Repeat(strings.Repeat("a", 61)+".", 4) + strings.Repeat("a", 5)
	assert.Len(t, long, 253)
	assert.True(t, ValidName(long, false))
	assert.False(t, ValidName(long+"b", false))
}

func TestSetClusterDNS_dropsBadNames(t *testing.T) {
	s := NewServer(nil, "ns", nil)
	err := s.SetClusterDNS(t.Context(), &manager.DNS{
		ClusterDomain:   "Cluster.Local.",
		IncludeSuffixes: []string{".Good", "../evil", "a/b", "x\ny"},
		ExcludeSuffixes: []string{".ok", "bad/../x"},
	}, s.VIFAddress)
	require.NoError(t, err)
	assert.Equal(t, "cluster.local.", s.clusterDomain)
	assert.Equal(t, []string{".good"}, s.IncludeSuffixes)
	assert.Equal(t, []string{".ok"}, s.ExcludeSuffixes)
}

// A cluster test cannot make a traffic-manager send a ClusterInfo without dns.
func TestSetClusterDNS_nil(t *testing.T) {
	s := NewServer(nil, "ns", nil)
	require.NoError(t, s.SetClusterDNS(t.Context(), nil, s.VIFAddress))
	assert.Equal(t, defaultClusterDomain, s.clusterDomain)
}

func TestSetClusterDNS_badClusterDomain(t *testing.T) {
	s := NewServer(nil, "ns", nil)
	for _, d := range []string{"", "a/../b", "x\ny", "../.."} {
		require.Error(t, s.SetClusterDNS(t.Context(), &manager.DNS{ClusterDomain: d}, s.VIFAddress), d)
	}
	assert.Equal(t, defaultClusterDomain, s.clusterDomain)
}

func TestSetTopLevelDomainsAndSearchPath_dropsBadNames(t *testing.T) {
	s := NewServer(nil, "ns", nil)
	s.SetTopLevelDomainsAndSearchPath(t.Context(), []string{"svc", "../x", "a/b"}, "../ns")
	das := <-s.nsAndDomainsCh
	assert.Equal(t, []string{"svc"}, das.domains)
	assert.Empty(t, das.namespace)
}

func TestResolverFilePath(t *testing.T) {
	p, err := resolverFilePath("/etc/resolver", "cluster.local")
	require.NoError(t, err)
	assert.Equal(t, "/etc/resolver/telepresence.cluster.local", p)
	for _, d := range []string{"x/../../y", "a/b", "../../etc/passwd", "/abs"} {
		_, err = resolverFilePath("/etc/resolver", d)
		assert.Error(t, err, d)
	}
}
