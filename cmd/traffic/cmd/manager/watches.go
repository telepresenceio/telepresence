package manager

type Watches map[string]chan<- struct{}

func NewWatches() *Watches {
	res := make(Watches)
	return &res
}

func (w Watches) Subscribe(id string) <-chan struct{} {
	res := make(chan struct{}, 1)
	w[id] = res
	return res
}

func (w Watches) Unsubscribe(id string) {
	if ch, ok := w[id]; ok {
		close(ch)
	}
	delete(w, id)
}

func (w Watches) IsSubscribed(id string) bool {
	_, ok := w[id]
	return ok
}

func (w Watches) Notify(id string) {
	if ch, ok := w[id]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (w Watches) NotifyAll() {
	for _, ch := range w {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
