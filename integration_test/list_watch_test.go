package integration_test

import (
	"context"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/datawire/dlib/dcontext"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type list_watchSuite struct {
	itest.Suite
	itest.NamespacePair
}

func init() {
	itest.AddConnectedSuite("", func(h itest.NamespacePair) suite.TestingSuite {
		return &list_watchSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *list_watchSuite) Test_ListWatch() {
	svc := "echo-easy"

	teleListWatch := func(ctx context.Context, ch chan string) {
		stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--watch")
		ch <- stdout
	}

	s.Run("<ctrl>-C", func() {
		// Use a soft context to send a <ctrl>-c to telepresence in order to end it
		ctx := s.Context()
		soft, softCancel := context.WithCancel(dcontext.WithSoftness(ctx))
		// Use a channel to pretty print error
		ch := make(chan string)
		go teleListWatch(soft, ch)
		time.Sleep(time.Second)
		s.ApplyApp(ctx, svc, "deploy/"+svc)
		time.Sleep(time.Second)
		softCancel()
		stdout := <-ch
		s.Contains(stdout, svc)
	})
}
