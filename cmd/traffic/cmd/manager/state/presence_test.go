package state

import (
	"fmt"
	"testing"
	"time"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestPresence(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)

	p := NewState(ctx)

	now := time.Now()

	sa := p.AddClient(&rpc.ClientInfo{Name: "item-a"}, now)
	sb := p.AddClient(&rpc.ClientInfo{Name: "item-b"}, now)

	// A@0 B@0

	isPresent := func(sessionID string) bool {
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

	a.True(p.MarkSession(&rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: sa}}, now))
	a.True(p.MarkSession(&rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: sb}}, now))
	a.False(p.MarkSession(&rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: "c"}}, now))

	now = now.Add(time.Second)
	a.True(p.MarkSession(&rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: sb}}, now))
	sc := p.AddClient(&rpc.ClientInfo{Name: "item-c"}, now)

	// A@0 B@1 C@1

	a.True(isPresent(sa))
	a.True(isPresent(sb))
	a.True(isPresent(sc))
	a.False(isPresent("d"))

	collected := make([]string, 0, 3)
	for id, item := range p.GetAllClients() {
		collected = append(collected, fmt.Sprintf("%s/%v", id, item.Name))
	}
	a.Contains(collected, fmt.Sprintf("%s/item-a", sa))
	a.Contains(collected, fmt.Sprintf("%s/item-b", sb))
	a.Contains(collected, fmt.Sprintf("%s/item-c", sc))

	p.ExpireSessions(ctx, now, now)

	// B@1 C@1

	a.False(isPresent(sa))
	a.True(isPresent(sb))
	a.True(isPresent(sc))
	a.False(isPresent("d"))

	a.NoError(p.RemoveSession(ctx, sa))
	a.NoError(p.RemoveSession(ctx, sc))

	// B@1

	a.False(isPresent(sa))
	a.True(isPresent(sb))
	a.False(isPresent(sc))
	a.False(isPresent("d"))

	a.Panics(func() { p.(*state).addClient(sb, &rpc.ClientInfo{Name: "duplicate-item-b"}, now) })
}
