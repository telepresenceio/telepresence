package state

import (
	"fmt"
	"testing"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func TestPresence(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{})
	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
	p := NewState(ctx, g)

	now := time.Now()

	sa := p.AddClient(&rpc.ClientInfo{Name: "item-a"}, now)
	sb := p.AddClient(&rpc.ClientInfo{Name: "item-b"}, now)

	// A@0 B@0

	isPresent := func(sessionID tunnel.SessionID) bool {
		_, err := p.SessionDone(sessionID)
		return err == nil
	}

	a := assertNew(t)
	a.True(isPresent(sa))
	a.True(isPresent(sb))
	a.False(isPresent("c"))
	a.False(isPresent("d"))

	a.NotNil(p.GetClient(sa))
	a.Equal("item-a", p.GetClient(sa).Name)
	a.Nil(p.GetClient("c"))

	a.True(p.MarkSession(&rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: string(sa)}}, now))
	a.True(p.MarkSession(&rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: string(sb)}}, now))
	a.False(p.MarkSession(&rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: "c"}}, now))

	now = now.Add(time.Second)
	a.True(p.MarkSession(&rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: string(sb)}}, now))
	sc := p.AddClient(&rpc.ClientInfo{Name: "item-c"}, now)

	// A@0 B@1 C@1

	a.True(isPresent(sa))
	a.True(isPresent(sb))
	a.True(isPresent(sc))
	a.False(isPresent("d"))

	collected := make([]string, 0, 3)
	p.EachClient(func(id tunnel.SessionID, item *ClientSession) bool {
		collected = append(collected, fmt.Sprintf("%s/%v", id, item.Name))
		return true
	})
	a.Contains(collected, fmt.Sprintf("%s/item-a", sa))
	a.Contains(collected, fmt.Sprintf("%s/item-b", sb))
	a.Contains(collected, fmt.Sprintf("%s/item-c", sc))

	p.expireSessions(ctx, now, now)

	// B@1 C@1

	a.False(isPresent(sa))
	a.True(isPresent(sb))
	a.True(isPresent(sc))
	a.False(isPresent("d"))

	p.RemoveSession(ctx, sa)
	p.RemoveSession(ctx, sc)

	// B@1

	a.False(isPresent(sa))
	a.True(isPresent(sb))
	a.False(isPresent(sc))
	a.False(isPresent("d"))

	a.Panics(func() { p.addClient(sb, &rpc.ClientInfo{Name: "duplicate-item-b"}, now) })
}
