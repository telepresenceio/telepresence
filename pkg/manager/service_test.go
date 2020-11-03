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

	"github.com/datawire/telepresence2/pkg/manager"
	"github.com/datawire/telepresence2/pkg/rpc"
	"github.com/datawire/telepresence2/pkg/version"
)

func TestConnect(t *testing.T) {
	testClients := manager.GetTestClients(t)
	testAgents := manager.GetTestAgents(t)

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

	// Alice arrives and sees no agents

	alice, err := client.ArriveAsClient(ctx, testClients["alice"])
	a.NoError(err)

	aliceW, err := client.WatchAgents(ctx, alice)
	a.NoError(err)

	aSnap, err := aliceW.Recv()
	a.NoError(err)
	a.Len(aSnap.Agents, 0)

	// Hello's agent arrives

	hello, err := client.ArriveAsAgent(ctx, testAgents["hello"])
	a.NoError(err)

	_ = hello

	// Alice sees an agent

	aSnap, err = aliceW.Recv()
	a.NoError(err)
	a.Len(aSnap.Agents, 1)
	a.True(proto.Equal(testAgents["hello"], aSnap.Agents[0]))

	// Demo Deployment comes up with two Pods

	demo1, err := client.ArriveAsAgent(ctx, testAgents["demo1"])
	a.NoError(err)
	demo2, err := client.ArriveAsAgent(ctx, testAgents["demo2"])
	a.NoError(err)

	_ = demo1
	_ = demo2

	// Alice sees all the agents

	aSnap, err = aliceW.Recv()
	a.NoError(err)
	a.Len(aSnap.Agents, 2)

	aSnap, err = aliceW.Recv()
	a.NoError(err)
	a.Len(aSnap.Agents, 3)

	// Alice remains

	_, err = client.Remain(ctx, alice)
	a.NoError(err)

	// Alice creates an intercept and then removes it

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

	// Duplicate should error
	_, err = client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
		Session:       alice,
		InterceptSpec: spec,
	})
	a.Error(err)

	_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
		Session: alice,
		Name:    spec.Name,
	})
	a.NoError(err)

	t.Log("removed")

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
