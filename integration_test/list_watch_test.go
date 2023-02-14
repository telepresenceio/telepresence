package integration_test

import (
	"context"
	"time"

	"github.com/stretchr/testify/suite"

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

	s.Run("<ctrl>-C", func() {
		// Use a context to end tele list -w
		ctx := s.Context()
		cancelctx, cancel := context.WithCancel(ctx)
		ch := make(chan string)
		go func() {
			stdout, _, _ := itest.Telepresence(cancelctx, "list", "--namespace", s.AppNamespace(), "--output", "json-stream")
			ch <- stdout
		}()
		time.Sleep(time.Second)
		s.ApplyApp(ctx, svc, "deploy/"+svc)
		time.Sleep(time.Second)
		cancel()
		s.Contains(<-ch, svc)
	})
}
