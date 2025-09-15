package maps

import (
	"time"

	"github.com/puzpuzpuz/xsync/v4"
)

// GC removes entries from the provided xsync.Map at regular intervals based on a provided condition and stops when the done channel is closed.
func GC[K comparable, V any](m *xsync.Map[K, V], interval time.Duration, done <-chan struct{}, deleteWhen func(K, V) bool) {
	ticker := time.NewTicker(interval)
	for {
		select {
		case <-done:
			ticker.Stop()
			return
		case <-ticker.C:
			m.Range(func(k K, v V) bool {
				if deleteWhen(k, v) {
					m.Delete(k)
				}
				return true
			})
		}
	}
}
