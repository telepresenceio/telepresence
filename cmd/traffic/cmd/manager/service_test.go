package manager_test

import (
	"context"
	"log"
	"net"
	"testing"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/datawire/telepresence2/cmd/traffic/cmd/manager"
	testdata "github.com/datawire/telepresence2/cmd/traffic/cmd/manager/internal/test"
	rpc "github.com/datawire/telepresence2/pkg/rpc/manager"
	"github.com/datawire/telepresence2/pkg/version"
)

func TestConnect(t *testing.T) {
	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	version.Version = "testing"

	conn := getTestClientConn(t)
	defer conn.Close()

	client := rpc.NewManagerClient(conn)
	ctx := context.Background()

	a := assert.New(t)

	ver, err := client.Version(ctx, &empty.Empty{})
	a.NoError(err)
	a.Equal(version.Version, ver.Version)

	// Alice arrives and departs

	aliceDeparts, err := client.ArriveAsClient(ctx, testClients["alice"])
	a.NoError(err)

	_, err = client.Depart(ctx, aliceDeparts)
	a.NoError(err)

	// Alice arrives and sees no agents or intercepts

	alice, err := client.ArriveAsClient(ctx, testClients["alice"])
	a.NoError(err)

	aliceWA, err := client.WatchAgents(ctx, alice)
	a.NoError(err)

	aSnapA, err := aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 0)

	aliceWI, err := client.WatchIntercepts(ctx, alice)
	a.NoError(err)

	aSnapI, err := aliceWI.Recv()
	a.NoError(err)
	a.Len(aSnapI.Intercepts, 0)

	// Hello's agent arrives

	hello, err := client.ArriveAsAgent(ctx, testAgents["hello"])
	a.NoError(err)

	helloWI, err := client.WatchIntercepts(ctx, hello)
	a.NoError(err)

	hSnapI, err := helloWI.Recv()
	a.NoError(err)
	a.Len(hSnapI.Intercepts, 0)

	// Alice sees an agent

	aSnapA, err = aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 1)
	a.True(proto.Equal(testAgents["hello"], aSnapA.Agents[0]))

	// Demo Deployment comes up with two Pods

	demo1, err := client.ArriveAsAgent(ctx, testAgents["demo1"])
	a.NoError(err)

	demo1WI, err := client.WatchIntercepts(ctx, demo1)
	a.NoError(err)

	d1SnapI, err := demo1WI.Recv()
	a.NoError(err)
	a.Len(d1SnapI.Intercepts, 0)

	demo2, err := client.ArriveAsAgent(ctx, testAgents["demo2"])
	a.NoError(err)

	demo2WI, err := client.WatchIntercepts(ctx, demo2)
	a.NoError(err)

	d2SnapI, err := demo2WI.Recv()
	a.NoError(err)
	a.Len(d2SnapI.Intercepts, 0)

	// Alice sees all the agents

	aSnapA, err = aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 2)

	aSnapA, err = aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 3)

	// Alice remains

	_, err = client.Remain(ctx, alice)
	a.NoError(err)

	// Hello Pro's agent arrives and departs

	helloPro, err := client.ArriveAsAgent(ctx, testAgents["helloPro"])
	a.NoError(err)

	helloProWI, err := client.WatchIntercepts(ctx, helloPro)
	a.NoError(err)

	hPSnapI, err := helloProWI.Recv()
	a.NoError(err)
	a.Len(hPSnapI.Intercepts, 0)

	aSnapA, err = aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 4)

	_, err = client.Depart(ctx, helloPro)
	a.NoError(err)

	aSnapA, err = aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 3)

	// Alice creates an intercept

	spec := &rpc.InterceptSpec{
		Name:       "first",
		Client:     testClients["alice"].Name,
		Agent:      testAgents["hello"].Name,
		Mechanism:  "tcp",
		Additional: "",
		TargetHost: "asdf",
		TargetPort: 9876,
	}

	first, err := client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
		Session:       alice,
		InterceptSpec: spec,
	})
	a.NoError(err)
	a.True(proto.Equal(spec, first.Spec))

	aSnapI, err = aliceWI.Recv()
	a.NoError(err)
	a.Len(aSnapI.Intercepts, 1)
	a.Equal(rpc.InterceptDispositionType_WAITING, aSnapI.Intercepts[0].Disposition)

	hSnapI, err = helloWI.Recv()
	a.NoError(err)
	a.Len(hSnapI.Intercepts, 1)
	a.Equal(rpc.InterceptDispositionType_WAITING, hSnapI.Intercepts[0].Disposition)

	// Hello's agent reviews the intercept

	_, err = client.ReviewIntercept(ctx, &rpc.ReviewInterceptRequest{
		Session:     hello,
		Id:          hSnapI.Intercepts[0].Id,
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		Message:     "okay!",
	})
	a.NoError(err)

	// Causing the intercept to go active with a port assigned

	aSnapI, err = aliceWI.Recv()
	a.NoError(err)
	a.Len(aSnapI.Intercepts, 1)
	a.Equal(rpc.InterceptDispositionType_ACTIVE, aSnapI.Intercepts[0].Disposition)
	a.NotEqual(0, aSnapI.Intercepts[0].ManagerPort)

	hSnapI, err = helloWI.Recv()
	a.NoError(err)
	a.Len(hSnapI.Intercepts, 1)
	a.Equal(rpc.InterceptDispositionType_ACTIVE, hSnapI.Intercepts[0].Disposition)
	a.NotEqual(0, hSnapI.Intercepts[0].ManagerPort)

	// Creating a duplicate intercept yields an error

	_, err = client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
		Session:       alice,
		InterceptSpec: spec,
	})
	a.Error(err)

	// Alice removes the intercept

	_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
		Session: alice,
		Name:    spec.Name,
	})
	a.NoError(err)

	aSnapI, err = aliceWI.Recv()
	a.NoError(err)
	a.Len(aSnapI.Intercepts, 0)

	hSnapI, err = helloWI.Recv()
	a.NoError(err)
	a.Len(hSnapI.Intercepts, 0)

	// Removing a bogus intercept yields an error

	_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
		Session: alice,
		Name:    spec.Name, // no longer present, right?
	})
	a.Error(err)

	_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
		Session: aliceDeparts, // no longer a valid session, right?
		Name:    spec.Name,    // doesn't matter...
	})
	a.Error(err)
}

func getTestClientConn(t *testing.T) *grpc.ClientConn {
	const bufsize = 64 * 1024

	lis := bufconn.Listen(bufsize)
	bufDialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	ctx := context.Background()

	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(bufDialer), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}

	s := grpc.NewServer()
	rpc.RegisterManagerServer(s, manager.NewManager(ctx))
	go func() {
		if err := s.Serve(lis); err != nil {
			log.Fatalf("Server exited with error: %v", err)
		}
	}()

	return conn
}
