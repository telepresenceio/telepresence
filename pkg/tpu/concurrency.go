package tpu

type empty interface{}

// Semaphore

type Semaphore chan empty

func NewSemaphore(n int) Semaphore {
	sem := make(Semaphore, n)
	for i := 0; i < n; i++ {
		sem.Release()
	}
	return sem
}

func (s Semaphore) Acquire() {
	<-s
}

func (s Semaphore) Release() {
	s <- nil
}

// Latch

type Latch struct {
	ch    chan empty
	count int
}

func NewLatch(n int) Latch {
	return Latch{
		ch:    make(chan empty),
		count: n,
	}
}

func (l Latch) Notify() {
	l.ch <- nil
}

func (l Latch) Wait() {
	for l.count > 0 {
		<-l.ch
		l.count -= 1
	}
}
