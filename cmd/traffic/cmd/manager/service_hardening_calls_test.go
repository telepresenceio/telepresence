package manager

import (
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
)

func TestLookupDNS_InvalidNames(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModePermissive
	})
	pctx := auth.WithPrincipal(sctx, &auth.Principal{Username: "alice", UID: "alice-uid"})
	sess, err := mgr.ArriveAsClient(pctx, testdata.GetTestClients(t)["alice"])
	req.NoError(err)

	types := []uint16{
		dns.TypeA, dns.TypeAAAA, dns.TypePTR, dns.TypeCNAME, dns.TypeMX, dns.TypeNS, dns.TypeSRV, dns.TypeTXT, 9999,
	}
	for _, name := range []string{"", ".", "a", "_a._b", "no-dot"} {
		for _, qType := range types {
			req.NotPanics(func() {
				_, err := mgr.LookupDNS(pctx, &rpc.DNSRequest{Session: sess, Name: name, Type: uint32(qType)})
				req.Error(err, "name %q type %d", name, qType)
				req.Equal(codes.InvalidArgument, status.Code(err), "name %q type %d", name, qType)
			})
		}
	}
}

func TestGetIntercept_RequiresOwnedSession(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModePermissive
	})
	clients := testdata.GetTestClients(t)
	actx := auth.WithPrincipal(sctx, &auth.Principal{Username: "alice", UID: "alice-uid"})
	bctx := auth.WithPrincipal(sctx, &auth.Principal{Username: "bob", UID: "bob-uid"})
	aliceSess, err := mgr.ArriveAsClient(actx, clients["alice"])
	req.NoError(err)
	bobSess, err := mgr.ArriveAsClient(bctx, clients["bob"])
	req.NoError(err)

	bobIntercept := &rpc.InterceptInfo{
		Id: bobSess.SessionId + ":bob-intercept",
		Spec: &rpc.InterceptSpec{
			Name:      "bob-intercept",
			Client:    clients["bob"].Name,
			Agent:     "hello",
			Namespace: "default",
			Mechanism: "tcp",
		},
		Disposition:   rpc.InterceptDispositionType_WAITING,
		ClientSession: bobSess,
		Environment:   map[string]string{"SECRET": "value"},
	}
	req.NoError(mgr.State().RestoreIntercepts(sctx, []*rpc.InterceptInfo{bobIntercept}, time.Now()))

	// Bob reads his own intercept by name.
	ii, err := mgr.GetIntercept(bctx, &rpc.GetInterceptRequest{Session: bobSess, Name: "bob-intercept"})
	req.NoError(err)
	req.Equal(bobIntercept.Id, ii.Id)

	// No session: the full intercept ID is not a way in.
	_, err = mgr.GetIntercept(actx, &rpc.GetInterceptRequest{Name: bobIntercept.Id})
	req.Error(err)
	_, err = mgr.GetIntercept(actx, &rpc.GetInterceptRequest{Session: &rpc.SessionInfo{}, Name: bobIntercept.Id})
	req.Error(err)

	// Alice's session, Bob's intercept ID.
	_, err = mgr.GetIntercept(actx, &rpc.GetInterceptRequest{Session: aliceSess, Name: bobIntercept.Id})
	req.Equal(codes.NotFound, status.Code(err))

	// Bob's session presented by Alice.
	_, err = mgr.GetIntercept(actx, &rpc.GetInterceptRequest{Session: bobSess, Name: "bob-intercept"})
	req.Error(err)
	req.NotEqual(codes.NotFound, status.Code(err))
}
