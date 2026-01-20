package state

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func TestPresence(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{})
	g := log.NewGroup(ctx)
	p := NewState(ctx, g, nil)

	now := time.Now()

	sa := p.AddClient(&rpc.ClientInfo{Name: "item-a"}, now)
	sb := p.AddClient(&rpc.ClientInfo{Name: "item-b"}, now)

	isPresent := func(sessionID tunnel.SessionID) bool {
		_, err := p.SessionDone(sessionID)
		return err == nil
	}

	a := assert.New(t)
	a.True(isPresent(sa))
	a.True(isPresent(sb))
	a.False(isPresent("c"))
	a.False(isPresent("d"))

	a.NotNil(p.GetClient(sa))
	a.Equal("item-a", p.GetClient(sa).Name)
	a.Nil(p.GetClient("c"))

	now = now.Add(time.Second)
	ca := p.GetClient(sa)
	require.NotNil(t, ca)

	a.True(ca.Mark(now))
	a.False(ca.Mark(now))

	cb := p.GetClient(sb)
	require.NotNil(t, cb)

	a.True(cb.Mark(now))
	a.False(cb.Mark(now))

	now = now.Add(time.Second)
	a.True(cb.Mark(now))
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

	p.expireSessions(now, now)

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
