package manager_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/datawire/telepresence2/pkg/manager"
	"github.com/stretchr/testify/assert"
)

func TestPresence(t *testing.T) {
	a := assert.New(t)

	removed := []string{}
	p := manager.NewPresence(context.Background(), func(_ context.Context, id string, item manager.Entity) {
		removed = append(removed, fmt.Sprintf("%s/%v", id, item))
	})

	now := time.Now()

	p.Add("a", "item-a", now)
	p.Add("b", "item-b", now)

	// A@0 B@0

	a.True(p.IsPresent("a"))
	a.True(p.IsPresent("b"))
	a.False(p.IsPresent("c"))
	a.False(p.IsPresent("d"))
	a.Equal([]string{}, removed)

	a.NotNil(p.Get("a"))
	a.Equal("item-a", p.Get("a").Item())
	a.Nil(p.Get("c"))

	a.NoError(p.Mark("a", now))
	a.NoError(p.Mark("b", now))
	a.Error(p.Mark("c", now))
	a.Error(p.Mark("d", now))

	now = now.Add(time.Second)
	a.NoError(p.Mark("b", now))
	p.Add("c", "item-c", now)

	// A@0 B@1 C@1

	a.True(p.IsPresent("a"))
	a.True(p.IsPresent("b"))
	a.True(p.IsPresent("c"))
	a.False(p.IsPresent("d"))
	a.Equal([]string{}, removed)

	p.Expire(now)

	// B@1 C@1

	a.False(p.IsPresent("a"))
	a.True(p.IsPresent("b"))
	a.True(p.IsPresent("c"))
	a.False(p.IsPresent("d"))
	a.Equal([]string{"a/item-a"}, removed)

	a.Nil(p.Remove("a"))
	a.NotNil(p.Remove("c"))

	// B@1

	a.False(p.IsPresent("a"))
	a.True(p.IsPresent("b"))
	a.False(p.IsPresent("c"))
	a.False(p.IsPresent("d"))
	a.Equal([]string{"a/item-a", "c/item-c"}, removed)

	a.Panics(func() { p.Add("b", "duplicate-item", now) })
}
