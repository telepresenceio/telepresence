package integration_test

import (
	"context"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) Test_ListWatch() {
	svc := "echo-easy"

	s.Run("<ctrl>-C", func() {
		// Use a context to end tele list -w
		ctx := s.Context()
		cancelctx, cancel := context.WithCancel(ctx)
		ch := make(chan string)
		go func() {
			stdout, _, _ := itest.Telepresence(cancelctx, "list", "--output", "json-stream")
			ch <- stdout
		}()
		time.Sleep(time.Second)
		s.ApplyApp(ctx, svc, "deploy/"+svc)
		defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)
		time.Sleep(time.Second)
		cancel()
		s.Contains(<-ch, svc)
	})
}
