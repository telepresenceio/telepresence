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
