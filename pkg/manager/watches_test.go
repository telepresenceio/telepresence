package manager_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/datawire/telepresence2/pkg/manager"
)

func receiver(ch <-chan struct{}) *bool {
	flag := false
	go func() {
		<-ch
		flag = true
	}()
	return &flag
}

func TestWatches(t *testing.T) {
	// This test is slow/dumb because it uses sleep calls to synchronize
	// goroutines. Only run it in CI.
	if os.Getenv("CI") == "" {
		t.Skip("Skipped! Use \"env CI=true go test [...]\" to run")
	}

	a := assert.New(t)
	sync := func() { time.Sleep(500 * time.Millisecond) }

	w := manager.NewWatches()

	a.False(w.IsSubscribed("a"))
	ch0 := w.Subscribe("a")
	a.True(w.IsSubscribed("a"))
	w.Unsubscribe("a")
	a.False(w.IsSubscribed("a"))
	<-ch0 // channel should be closed

	w.Unsubscribe("a")
	a.False(w.IsSubscribed("a"))

	chA := w.Subscribe("a")
	recvA := receiver(chA)

	chB := w.Subscribe("b")
	recvB := receiver(chB)

	sync()
	a.False(*recvA)
	a.False(*recvB)

	w.Notify("does not exist")
	sync()
	a.False(*recvA)
	a.False(*recvB)

	w.Notify("a")
	sync()
	a.True(*recvA)
	a.False(*recvB)

	// No receiver reading the channel now
	// Notify should not block
	w.Notify("a")
	sync()
	w.Notify("a")
	sync()
	recvA = receiver(chA)
	sync()
	a.True(*recvA)
	a.False(*recvB)

	// Create a new receiver
	recvA = receiver(chA)
	sync()
	a.False(*recvA)
	a.False(*recvB)

	w.NotifyAll()
	sync()
	a.True(*recvA)
	a.True(*recvB)
}
