package watchable

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/buraksezer/olric"
	"github.com/go-redis/redis/v8"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"google.golang.org/protobuf/proto"
)

type OlricMap[V Message] struct {
	dmap   olric.DMap
	pubSub *olric.PubSub
	name   string
	empty  V

	// lock only protects subscribers
	closed chan struct{} // can read from the channel while unlocked, IF you've already validated it's non-nil
	lock   sync.Mutex
	// subscribers map[<-chan Update[V]]chan<- Update[V]
	wg sync.WaitGroup
}

func NewOlricMap[V Message](name string, client olric.Client, empty V) (*OlricMap[V], error) {
	dmap, err := client.NewDMap(name)
	if err != nil {
		return nil, err
	}
	pubSub, err := client.NewPubSub()
	if err != nil {
		return nil, err
	}
	return &OlricMap[V]{
		dmap:   dmap,
		closed: make(chan struct{}),
		pubSub: pubSub,
		name:   name,
		// subscribers: make(map[<-chan Update[V]]chan<- Update[V]),
		empty: empty,
	}, nil
}

func (m *OlricMap[V]) unmarshal(resp *olric.GetResponse) (V, error) {
	value := proto.Clone(m.empty).(V)
	bytes, err := resp.Byte()
	if err != nil {
		return value, err
	}
	err = proto.Unmarshal(bytes, value)
	if err != nil {
		return value, err
	}
	return value, nil
}

// TODO: These functions will need errors and contexts.

func (m *OlricMap[V]) Store(key string, value V) {
	btes, err := proto.Marshal(value)
	if err != nil {
		panic(err)
	}
	err = m.dmap.Put(context.TODO(), key, btes)
	if err != nil {
		panic(err)
	}
	m.publishUpdate(Update[V]{
		Key:   key,
		Value: value,
	})
}

func (m *OlricMap[V]) Delete(key string) {
	_, err := m.dmap.Delete(context.TODO(), key)
	if err != nil {
		panic(err)
	}
	m.publishUpdate(Update[V]{
		Key:    key,
		Delete: true,
	})
}

func (m *OlricMap[V]) Load(key string) (V, bool) {
	value, err := m.dmap.Get(context.TODO(), key)
	if errors.Is(err, olric.ErrKeyNotFound) {
		var zero V
		return zero, false
	} else if err != nil {
		panic(err)
	}
	actual, err := m.unmarshal(value)
	if err != nil {
		panic(err)
	}
	return actual, true
}

func (m *OlricMap[V]) LoadAndDelete(key string) (V, bool) {
	value, err := m.dmap.GetPut(context.TODO(), key, []byte{})
	if errors.Is(err, olric.ErrKeyNotFound) {
		var zero V
		return zero, false
	} else if err != nil {
		panic(err)
	}
	actual, err := m.unmarshal(value)
	if err != nil {
		panic(err)
	}
	m.publishUpdate(Update[V]{
		Key:    key,
		Delete: true,
	})
	return actual, true
}

// These aren't really atomic.

func (m *OlricMap[V]) LoadAll() map[string]V {
	entries, err := m.dmap.Scan(context.TODO())
	if err != nil {
		panic(err)
	}
	defer entries.Close()
	result := make(map[string]V)
	for next := true; next; next = entries.Next() {
		key := entries.Key()
		v, err := m.dmap.Get(context.TODO(), key)
		if err != nil {
			// LOL
			continue
		}
		value, err := m.unmarshal(v)
		if err != nil {
			panic(err)
		}
		result[key] = value
	}
	return result
}

func (m *OlricMap[V]) LoadAllMatching(filter func(key string, value V) bool) map[string]V {
	entries, err := m.dmap.Scan(context.TODO())
	if err != nil {
		panic(err)
	}
	defer entries.Close()
	result := make(map[string]V)
	for next := true; next; next = entries.Next() {
		key := entries.Key()
		v, err := m.dmap.Get(context.TODO(), key)
		if err != nil {
			// LOL
			continue
		}
		value, err := m.unmarshal(v)
		if err != nil {
			panic(err)
		}
		if filter(key, value) {
			result[key] = value
		}
	}
	return result
}

func (m *OlricMap[V]) publishUpdate(update Update[V]) {
	str, err := json.Marshal(update)
	if err != nil {
		panic(err)
	}
	m.pubSub.Publish(context.TODO(), m.name, str)
}

func (m *OlricMap[V]) CompareAndSwap(key string, old, new V) bool {
	lock, err := m.dmap.Lock(context.TODO(), key, 5*time.Second)
	if err != nil {
		panic(err)
	}
	defer lock.Unlock(context.TODO())
	actual, err := m.dmap.Get(context.TODO(), key)
	if errors.Is(err, olric.ErrKeyNotFound) {
		return false
	} else if err != nil {
		panic(err)
	}
	actualValue, err := m.unmarshal(actual)
	if err != nil {
		panic(err)
	}
	if !proto.Equal(actualValue, old) {
		return false
	}
	err = m.dmap.Put(context.TODO(), key, new)
	if err != nil {
		panic(err)
	}
	m.publishUpdate(Update[V]{
		Key:   key,
		Value: new,
	})
	return true
}

func (m *OlricMap[V]) LoadOrStore(key string, value V) (actual V, loaded bool) {
	actual, loaded = m.Load(key)
	if loaded {
		return actual, true
	}
	m.Store(key, value)
	return value, false
}

func (m *OlricMap[V]) CountAll() int {
	scan, err := m.dmap.Scan(context.TODO())
	if err != nil {
		panic(err)
	}
	defer scan.Close()
	count := 0
	for next := true; next; next = scan.Next() {
		count++
	}
	return count
}

func chanWrap[V Message](ctx context.Context, upstream <-chan *redis.Message) <-chan Update[V] {
	downstream := make(chan Update[V])
	go func() {
		defer close(downstream)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-upstream:
				if !ok {
					return
				}
				var update Update[V]
				err := json.Unmarshal([]byte(msg.Payload), &update)
				if err != nil {
					panic(err)
				}
				downstream <- update
			}
		}
	}()
	return downstream
}

func (tm *OlricMap[V]) SubscribeSubset(ctx context.Context, include func(string, V) bool) <-chan Snapshot[V] {
	subscription := tm.pubSub.Subscribe(ctx, tm.name)
	downstream := make(chan Snapshot[V])
	initial := tm.LoadAllMatching(include)
	tm.wg.Add(1)
	go tm.coalesce(ctx, include, subscription, downstream, initial)

	return downstream
}

func (tm *OlricMap[V]) Subscribe(ctx context.Context) <-chan Snapshot[V] {
	return tm.SubscribeSubset(ctx, func(string, V) bool { return true })
}

func (tm *OlricMap[V]) Close() {
	select {
	case <-tm.closed:
		return
	default:
	}
	tm.lock.Lock()
	close(tm.closed)
	tm.lock.Unlock()
	tm.wg.Wait()
}

func (tm *OlricMap[V]) coalesce(
	ctx context.Context,
	includep func(string, V) bool,
	subscription *redis.PubSub,
	downstream chan<- Snapshot[V],
	initialSnapshot map[string]V,
) {
	defer tm.wg.Done()
	defer subscription.Close()
	defer close(downstream)
	uCtx, uCancel := context.WithCancel(ctx)
	upstream := chanWrap[V](uCtx, subscription.Channel())

	var shutdown func()
	shutdown = func() {
		shutdown = func() {} // Make this function an empty one after first run to prevent calling the following goroutine multiple times
		// Do this asynchronously because getting the lock might block a .Store() that's
		// waiting on us to read from 'upstream'!  We don't need to worry about separately
		// waiting for this goroutine because we implicitly do that when we drain
		// 'upstream'.
		uCancel()
	}

	// Cur is a snapshot of the current state all the map according to all MAPTYPEUpdates we've
	// received from 'upstream', with any entries removed that do not satisfy the predicate
	// 'includep'.
	cur := make(map[string]V)
	for k, v := range initialSnapshot {
		if includep(k, v) {
			cur[k] = v
		}
	}

	snapshot := Snapshot[V]{
		// snapshot.State is a copy of 'cur' that we send to the 'downstream' channel.  We
		// don't send 'cur' directly because we're necessarily in a separate goroutine from
		// the reader of 'downstream', and map gets/sets aren't thread-safe, so we'd risk
		// memory corruption with our updating of 'cur' and the reader's accessing of 'cur'.
		// snapshot.State gets set to 'nil' when we need to do a read before we can write to
		// 'downstream' again.
		State:   maps.Copy(cur),
		Updates: nil,
	}

	// applyUpdate applies an update to 'cur', and updates 'snapshot.State' as nescessary.
	applyUpdate := func(update Update[V]) {
		if update.Delete || !includep(update.Key, update.Value) {
			if old, haveOld := cur[update.Key]; haveOld {
				update.Delete = true
				update.Value = old
				snapshot.Updates = append(snapshot.Updates, update)
				delete(cur, update.Key)
				if snapshot.State != nil {
					delete(snapshot.State, update.Key)
				} else {
					snapshot.State = make(map[string]V, len(cur))
					for k, v := range cur {
						snapshot.State[k] = v
					}
				}
			}
		} else {
			if old, haveOld := cur[update.Key]; !haveOld || !proto.Equal(old, update.Value) {
				snapshot.Updates = append(snapshot.Updates, update)
				cur[update.Key] = update.Value
				if snapshot.State != nil {
					snapshot.State[update.Key] = update.Value
				} else {
					snapshot.State = maps.Copy(cur)
				}
			}
		}
	}
	closeCh := tm.closed
	doneCh := ctx.Done()
	for {
		if snapshot.State == nil {
			select {
			case <-doneCh:
				shutdown()
				doneCh = nil
			case <-closeCh:
				shutdown()
				closeCh = nil
			case update, readOK := <-upstream:
				if !readOK {
					return
				}
				applyUpdate(update)
			}
		} else {
			// Same as above, but with an additional "downstream <- snapshot" case.
			select {
			case <-doneCh:
				shutdown()
				doneCh = nil
			case <-closeCh:
				shutdown()
				closeCh = nil
			case update, readOK := <-upstream:
				if !readOK {
					return
				}
				applyUpdate(update)
			case downstream <- snapshot:
				snapshot = Snapshot[V]{}
			}
		}
	}
}
