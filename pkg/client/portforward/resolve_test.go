package portforward

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/resolver"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestParseAddr_NoLookupMarker(t *testing.T) {
	kind, name, namespace, port, podID, err := parseAddr("pod/traffic-manager-0.ambassador:8081~!")
	require.NoError(t, err)
	assert.Equal(t, "pod", kind)
	assert.Equal(t, "traffic-manager-0", name)
	assert.Equal(t, "ambassador", namespace)
	assert.Equal(t, "8081", port)
	assert.Equal(t, k8sTypes.UID(NoLookupMarker), podID)
}

func TestParseAddr_NoUID(t *testing.T) {
	// No "~" suffix at all still parses with an empty podID (the GetPod-lookup form).
	kind, name, namespace, port, podID, err := parseAddr("pod/echo-abc123.default:9900")
	require.NoError(t, err)
	assert.Equal(t, "pod", kind)
	assert.Equal(t, "echo-abc123", name)
	assert.Equal(t, "default", namespace)
	assert.Equal(t, "9900", port)
	assert.Equal(t, k8sTypes.UID(""), podID)
}

func TestParsePodAddr_NoLookupMarkerYieldsEmptyPodID(t *testing.T) {
	pa, err := parsePodAddr("pod/traffic-manager-0.ambassador:8081~!")
	require.NoError(t, err)
	assert.Equal(t, "traffic-manager-0", pa.Name)
	assert.Equal(t, "ambassador", pa.Namespace)
	assert.Equal(t, uint16(8081), pa.Port)
	assert.Equal(t, k8sTypes.UID(""), pa.PodID)
}

func TestPodAddress_StringAndAddrFor_NoLookup(t *testing.T) {
	pa := &PodAddress{Name: "traffic-manager-0", Namespace: "ambassador", Port: 8081}
	assert.Equal(t, "pod/traffic-manager-0.ambassador:8081~!", pa.String())
	assert.Equal(t, "pod/traffic-manager-0.ambassador:15007~!", pa.AddrFor(15007))

	// The String() output round-trips back through parsePodAddr the same
	// way the k8spf resolver feeds it to the dialer.
	reparsed, err := parsePodAddr(pa.String())
	require.NoError(t, err)
	assert.Equal(t, pa.Name, reparsed.Name)
	assert.Equal(t, pa.Namespace, reparsed.Namespace)
	assert.Equal(t, pa.Port, reparsed.Port)
	assert.Equal(t, k8sTypes.UID(""), reparsed.PodID)
}

func TestPodAddress_StringAndAddrFor_WithUID(t *testing.T) {
	pa := &PodAddress{Name: "traffic-manager-abc123", Namespace: "ambassador", Port: 8081, PodID: "11111111-1111-1111-1111-111111111111"}
	assert.Equal(t, "pod/traffic-manager-abc123.ambassador:8081~11111111-1111-1111-1111-111111111111", pa.String())
	assert.Equal(t, "pod/traffic-manager-abc123.ambassador:15007~11111111-1111-1111-1111-111111111111", pa.AddrFor(15007))
}

// TestResolve_NoLookupMarker_NoAPICall verifies the known-name marker
// short-circuits resolve() with ctx carrying no k8sapi client to call.
func TestResolve_NoLookupMarker_NoAPICall(t *testing.T) {
	ctx := t.Context()
	pa, err := resolve(ctx, "pod/traffic-manager-0.ambassador:8081~!")
	require.NoError(t, err)
	assert.Equal(t, "traffic-manager-0", pa.Name)
	assert.Equal(t, "ambassador", pa.Namespace)
	assert.Equal(t, uint16(8081), pa.Port)
	assert.Equal(t, k8sTypes.UID(""), pa.PodID)
}

// TestResolve_UIDShortCircuit_NoAPICall verifies an address already
// carrying a real UID keeps resolving with no Kubernetes API call.
func TestResolve_UIDShortCircuit_NoAPICall(t *testing.T) {
	ctx := t.Context()
	pa, err := resolve(ctx, "pod/echo-abc123.default:9900~11111111-1111-1111-1111-111111111111")
	require.NoError(t, err)
	assert.Equal(t, "echo-abc123", pa.Name)
	assert.Equal(t, "default", pa.Namespace)
	assert.Equal(t, uint16(9900), pa.Port)
	assert.Equal(t, k8sTypes.UID("11111111-1111-1111-1111-111111111111"), pa.PodID)
}

// TestResolve_PodNoUID_FetchesPod verifies a UID-less pod address (no "~"
// suffix, not the "!" marker) still fetches the pod via GetPod instead of
// degrading into the no-lookup form.
func TestResolve_PodNoUID_FetchesPod(t *testing.T) {
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{Name: "echo-abc123", Namespace: "default", UID: "real-uid"},
		Spec: core.PodSpec{Containers: []core.Container{{
			Ports: []core.ContainerPort{{Name: "http", ContainerPort: 9900}},
		}}},
	}
	ctx := k8sapi.WithK8sInterface(t.Context(), fake.NewSimpleClientset(pod))
	pa, err := resolve(ctx, "pod/echo-abc123.default:9900")
	require.NoError(t, err)
	assert.Equal(t, "echo-abc123", pa.Name)
	assert.Equal(t, "default", pa.Namespace)
	assert.Equal(t, uint16(9900), pa.Port)
	assert.Equal(t, k8sTypes.UID("real-uid"), pa.PodID)
}

// TestResolve_SvcForm_StillResolvesViaService verifies that the "svc/" form
// keeps going through ResolveSvcToPod (services get + pod list), unaffected
// by the no-lookup marker.
func TestResolve_SvcForm_StillResolvesViaService(t *testing.T) {
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: "traffic-manager", Namespace: "ambassador"},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "traffic-manager"},
			Ports:    []core.ServicePort{{Name: "api", Port: 8081, TargetPort: intstr.FromInt32(8081)}},
		},
	}
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{Name: "traffic-manager-xyz", Namespace: "ambassador", UID: "svc-uid", Labels: map[string]string{"app": "traffic-manager"}},
		Spec:       core.PodSpec{Containers: []core.Container{{Ports: []core.ContainerPort{{ContainerPort: 8081}}}}},
		Status:     core.PodStatus{Phase: core.PodRunning},
	}
	ctx := k8sapi.WithK8sInterface(t.Context(), fake.NewSimpleClientset(svc, pod))
	pa, err := resolve(ctx, "svc/traffic-manager.ambassador:8081")
	require.NoError(t, err)
	assert.Equal(t, "traffic-manager-xyz", pa.Name)
	assert.Equal(t, k8sTypes.UID("svc-uid"), pa.PodID)
}

// captureClientConn records the resolver state UpdateState pushes, so a
// test can assert what Build produced.
type captureClientConn struct {
	resolver.ClientConn
	state resolver.State
}

func (c *captureClientConn) UpdateState(s resolver.State) error {
	c.state = s
	return nil
}

// TestResolverBuild_UIDSeparatorSurvivesTargetParsing verifies the "~<uid>"
// suffix stays in the URL path through the url.Parse grpc applies to dial
// targets, so the no-lookup form doesn't degrade into a refused GetPod call.
func TestResolverBuild_UIDSeparatorSurvivesTargetParsing(t *testing.T) {
	u, err := url.Parse(K8sPFScheme + ":///pod/traffic-manager-0.ambassador:8081" + UIDSeparator + NoLookupMarker)
	require.NoError(t, err)
	require.Empty(t, u.Fragment)
	// No Kubernetes interface in the context: the test fails with a panic
	// or error unless the no-lookup short-circuit is taken.
	b := NewResolver(t.Context())
	cc := &captureClientConn{}
	_, err = b.Build(resolver.Target{URL: *u}, cc, resolver.BuildOptions{})
	require.NoError(t, err)
	if cc.state.ServiceConfig != nil {
		require.NoError(t, cc.state.ServiceConfig.Err)
	}
	require.Len(t, cc.state.Addresses, 1)
	assert.Equal(t, "pod/traffic-manager-0.ambassador:8081~!", cc.state.Addresses[0].Addr)
}
