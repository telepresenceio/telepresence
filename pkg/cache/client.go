package cache

import (
	"github.com/puzpuzpuz/xsync/v4"
)

type Entry[K comparable, V any] interface {
	Key() K
	Value() V
}

type ClientMap[K comparable, V any] struct {
	*xsync.Map[K, V]
}

func (c *ClientMap[K, V]) Watch(doneCh <-chan struct{}, deltaCh <-chan Delta[K, V], onChanges func() error) error {
	for {
		select {
		case <-doneCh:
			return nil
		case delta, ok := <-deltaCh:
			if !ok {
				return nil
			}
			for k, v := range delta.Upserts {
				c.Store(k, v)
			}
			for k := range delta.Removals {
				c.Delete(k)
			}
			if onChanges != nil {
				err := onChanges()
				if err != nil {
					return err
				}
			}
		}
	}
}

func NewClientMap[K comparable, V any](options ...func(config *xsync.MapConfig)) *ClientMap[K, V] {
	return &ClientMap[K, V]{Map: xsync.NewMap[K, V](options...)}
}

type Server[K comparable, V any] interface {
	Subscribe(done <-chan struct{}, filter func(K, V) bool) <-chan Delta[K, V]
}
