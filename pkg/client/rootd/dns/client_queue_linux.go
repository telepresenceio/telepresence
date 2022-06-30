package dns

import (
	"time"

	"github.com/miekg/dns"
)

// Most of this was aped off the docs at https://pkg.go.dev/container/heap@go1.17.8

type waitingClient struct {
	returnCh    chan *dns.Conn
	doneCh      <-chan struct{}
	arrivalTime time.Time
	index       int // The index of the item in the heap.
}

// A clientQueue implements heap.Interface and holds waitingClients.
type clientQueue []*waitingClient

func (pq clientQueue) Len() int { return len(pq) }

func (pq clientQueue) Less(i, j int) bool {
	return pq[i].arrivalTime.Before(pq[j].arrivalTime)
}

func (pq clientQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *clientQueue) Push(x any) {
	n := len(*pq)
	item := x.(*waitingClient)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *clientQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	*pq = old[0 : n-1]
	return item
}
