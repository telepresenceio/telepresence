package dns

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestConnPoolConcurrency(t *testing.T) {
	const (
		TOTAL_THREADS       = 3
		REQUESTS_PER_THREAD = 5
		TIMEOUT_S           = 8
	)
	ctx := context.Background()
	dc := &dns.Client{
		Net:     "udp",
		Timeout: TIMEOUT_S * time.Second,
	}
	pool := NewConnPool("8.8.8.8", 5)
	defer pool.Close()
	errors := make(chan error)
	wg := &sync.WaitGroup{}
	wg.Add(TOTAL_THREADS)
	for i := 0; i < TOTAL_THREADS; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < REQUESTS_PER_THREAD; j++ {
				msg := new(dns.Msg)
				domain := fmt.Sprintf("dns-test-%d.preview.edgestack.me.", idx)
				msg.SetQuestion(domain, dns.TypeMX)
				ctx, cancel := context.WithTimeout(ctx, TIMEOUT_S*time.Second)
				_, _, err := pool.Exchange(ctx, dc, msg)
				cancel()
				errors <- err
			}
		}(i)
	}
	go func() {
		wg.Wait()
		close(errors)
	}()
	for err := range errors {
		if err != nil {
			t.Error(err)
		}
	}
}
