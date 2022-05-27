package state_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/state"
)

func TestPresence(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	a := assertNew(t)

	p := state.NewState(ctx)

	now := time.Now()

	p.Add("a", "item-a", now)
	p.Add("b", "item-b", now)

	// A@0 B@0

	a.True(p.IsPresent("a"))
	a.True(p.IsPresent("b"))
	a.False(p.IsPresent("c"))
	a.False(p.IsPresent("d"))

	a.NotNil(p.Get("a"))
	a.Equal("item-a", p.Get("a").Item())
	a.Nil(p.Get("c"))

	a.True(p.Mark("a", now))
	a.True(p.Mark("b", now))
	a.False(p.Mark("c", now))
	a.False(p.Mark("d", now))

	now = now.Add(time.Second)
	a.True(p.Mark("b", now))
	p.Add("c", "item-c", now)

	// A@0 B@1 C@1

	a.True(p.IsPresent("a"))
	a.True(p.IsPresent("b"))
	a.True(p.IsPresent("c"))
	a.False(p.IsPresent("d"))

	collected := []string{}
	for id, item := range p.GetAllClients() {
		collected = append(collected, fmt.Sprintf("%s/%v", id, item.Name))
	}
	a.Contains(collected, "a/item-a")
	a.Contains(collected, "b/item-b")
	a.Contains(collected, "c/item-c")

	p.ExpireSessions(ctx, now, now)

	// B@1 C@1

	a.False(p.IsPresent("a"))
	a.True(p.IsPresent("b"))
	a.True(p.IsPresent("c"))
	a.False(p.IsPresent("d"))

	p.RemoveSession(ctx, "a")
	p.RemoveSession(ctx, "c")

	// B@1

	a.False(p.IsPresent("a"))
	a.True(p.IsPresent("b"))
	a.False(p.IsPresent("c"))
	a.False(p.IsPresent("d"))

	a.Panics(func() { p.Add("b", "duplicate-item", now) })
}
