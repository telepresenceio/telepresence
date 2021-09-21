//go:build ignore
// +build ignore

package watchable

import (
	"context"
	"sync"

	"google.golang.org/protobuf/proto"

	"VALPKG"
)

// MAPTYPEUpdate describes a mutation made to a MAPTYPE.
type MAPTYPEUpdate struct {
	Key    string
	Delete bool // Whether this is deleting the entry for .Key, or setting it to .Value.
	Value  VALTYPE
}

// MAPTYPESnapshot contains a snapshot of the current state of a MAPTYPE, as well as a list of
// changes that have happened since the last snapshot.
type MAPTYPESnapshot struct {
	// State is the current state of the snapshot.
	State map[string]VALTYPE
	// Updates is the list of mutations that have happened since the previous snapshot.
	// Mutations that delete a value have .Delete=true, and .Value set to the value that was
	// deleted.  No-op updates are not included (i.e., setting something to its current value,
	// or deleting something that does not exist).
	Updates []MAPTYPEUpdate
}

// MAPTYPE is a wrapper around map[string]VALTYPE that is very similar to sync.Map, and that
// provides the additional features that:
//
// 1. it is thread-safe (compared to a bare map)
// 2. it provides type safety (compared to a sync.Map)
// 3. it provides a compare-and-swap operation
// 4. you can Subscribe to either the whole map or just a subset of the map to watch for updates.
//    This gives you complete snapshots, deltas, and coalescing of rapid updates.
type MAPTYPE struct {
	lock sync.RWMutex
	// things guarded by 'lock'
	close       chan struct{} // can read from the channel while unlocked, IF you've already validated it's non-nil
	value       map[string]VALTYPE
	subscribers map[<-chan MAPTYPEUpdate]chan<- MAPTYPEUpdate // readEnd ↦ writeEnd

	// not guarded by 'lock'
	wg sync.WaitGroup
}

func (tm *MAPTYPE) unlockedInit() {
	if tm.close == nil {
		tm.close = make(chan struct{})
		tm.value = make(map[string]VALTYPE)
		tm.subscribers = make(map[<-chan MAPTYPEUpdate]chan<- MAPTYPEUpdate)
	}
}

func (tm *MAPTYPE) unlockedIsClosed() bool {
	select {
	case <-tm.close:
		return true
	default:
		return false
	}
}

// LoadAll returns a deepcopy of all key/value pairs in the map.
func (tm *MAPTYPE) LoadAll() map[string]VALTYPE {
	tm.lock.RLock()
	defer tm.lock.RUnlock()
	ret := make(map[string]VALTYPE, len(tm.value))
	for k, v := range tm.value {
		ret[k] = proto.Clone(v).(VALTYPE)
	}
	return ret
}

// Load returns a deepcopy of the value for a specific key.
func (tm *MAPTYPE) Load(key string) (value VALTYPE, ok bool) {
	tm.lock.RLock()
	defer tm.lock.RUnlock()
	ret, ok := tm.value[key]
	if !ok {
		return nil, false
	}
	return proto.Clone(ret).(VALTYPE), true
}

// Store sets a key sets the value for a key.  This blocks forever if .Close() has already been
// called.
func (tm *MAPTYPE) Store(key string, val VALTYPE) {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	tm.unlockedStore(key, val)
}

// LoadOrStore returns the existing value for the key if present.  Otherwise, it stores and returns
// the given value. The 'loaded' result is true if the value was loaded, false if stored.
//
// If the value does need to be stored, all the same blocking semantics as .Store() apply
func (tm *MAPTYPE) LoadOrStore(key string, val VALTYPE) (value VALTYPE, loaded bool) {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	loadedVal, loadedOK := tm.value[key]
	if loadedOK {
		return proto.Clone(loadedVal).(VALTYPE), true
	}
	tm.unlockedStore(key, val)
	return proto.Clone(val).(VALTYPE), false
}

// CompareAndSwap is the atomic equivalent of:
//
//     if loadedVal, loadedOK := m.Load(key); loadedOK && proto.Equal(loadedVal, old) {
//         m.Store(key, new)
//         return true
//     }
//     return false
func (tm *MAPTYPE) CompareAndSwap(key string, old, new VALTYPE) bool {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	if loadedVal, loadedOK := tm.value[key]; loadedOK && proto.Equal(loadedVal, old) {
		tm.unlockedStore(key, new)
		return true
	}
	return false
}

func (tm *MAPTYPE) unlockedStore(key string, val VALTYPE) {
	tm.unlockedInit()
	if tm.unlockedIsClosed() {
		// block forever
		tm.lock.Unlock()
		select {}
	}

	tm.value[key] = val
	for _, subscriber := range tm.subscribers {
		subscriber <- MAPTYPEUpdate{
			Key:   key,
			Value: proto.Clone(val).(VALTYPE),
		}
	}
}

// Delete deletes the value for a key.  This blocks forever if .Close() has already been called.
func (tm *MAPTYPE) Delete(key string) {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	tm.unlockedDelete(key)
}

func (tm *MAPTYPE) unlockedDelete(key string) {
	tm.unlockedInit()
	if tm.unlockedIsClosed() {
		// block forever
		tm.lock.Unlock()
		select {}
	}

	if tm.value == nil {
		return
	}
	delete(tm.value, key)
	for _, subscriber := range tm.subscribers {
		subscriber <- MAPTYPEUpdate{
			Key:    key,
			Delete: true,
		}
	}
}

// LoadAndDelete deletes the value for a key, returning a deepcopy of the previous value if any.
// The 'loaded' result reports whether the key was present.
//
// If the value does need to be deleted, all the same blocking semantics as .Delete() apply.
func (tm *MAPTYPE) LoadAndDelete(key string) (value VALTYPE, loaded bool) {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	loadedVal, loadedOK := tm.value[key]
	if !loadedOK {
		return nil, false
	}

	tm.unlockedDelete(key)

	return proto.Clone(loadedVal).(VALTYPE), true
}

// Close marks the map as "finished", all subscriber channels are closed and further mutations are
// forbidden.
//
// After .Close() is called, any calls to .Store() will block forever, and any calls to .Subscribe()
// will return an already-closed channel.
//
// .Load() and .LoadAll() calls will continue to work normally after .Close() has been called.
func (tm *MAPTYPE) Close() {
	tm.lock.Lock()

	tm.unlockedInit()
	if !tm.unlockedIsClosed() {
		close(tm.close)
	}
	tm.lock.Unlock()
	tm.wg.Wait()
}

// internalSubscribe returns a channel (that blocks on both ends), that is written to on each map
// update.  If the map is already Close()ed, then this returns nil.
func (tm *MAPTYPE) internalSubscribe(ctx context.Context) <-chan MAPTYPEUpdate {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	tm.unlockedInit()

	ret := make(chan MAPTYPEUpdate)
	if tm.unlockedIsClosed() {
		return nil
	}
	tm.subscribers[ret] = ret
	return ret
}

// Subscribe returns a channel that will emits a complete snapshot of the map immediately after the
// call to Subscribe(), and then whenever the map changes.  Updates are coalesced; if you do not
// need to worry about reading from the channel faster than you are able.  The snapshot will contain
// the full list of coalesced updates; the initial snapshot will contain 0 updates.  A read from the
// channel will block as long as there are no changes since the last read.
//
// The values in the snapshot are deepcopies of the actual values in the map, but values may be
// reused between snapshots; if you mutate a value in a snapshot, that mutation may erroneously
// persist in future snapshots.
//
// The returned channel will be closed when the Context is Done, or .Close() is called.  If .Close()
// has already been called, then an already-closed channel is returned.
func (tm *MAPTYPE) Subscribe(ctx context.Context) <-chan MAPTYPESnapshot {
	return tm.SubscribeSubset(ctx, func(string, VALTYPE) bool {
		return true
	})
}

// SubscribeSubset is like Subscribe, but the snapshot returned only includes entries that satisfy
// the 'include' predicate.  Mutations to entries that don't satisfy the predicate do not cause a
// new snapshot to be emitted.  If the value for a key changes from satisfying the predicate to not
// satisfying it, then this is treated as a delete operation, and a new snapshot is generated.
func (tm *MAPTYPE) SubscribeSubset(ctx context.Context, include func(string, VALTYPE) bool) <-chan MAPTYPESnapshot {
	upstream := tm.internalSubscribe(ctx)
	downstream := make(chan MAPTYPESnapshot)

	if upstream == nil {
		close(downstream)
		return downstream
	}

	tm.wg.Add(1)
	go tm.coalesce(ctx, include, upstream, downstream)

	return downstream
}

func (tm *MAPTYPE) coalesce(
	ctx context.Context,
	includep func(string, VALTYPE) bool,
	upstream <-chan MAPTYPEUpdate,
	downstream chan<- MAPTYPESnapshot,
) {
	defer tm.wg.Done()
	defer close(downstream)

	var shutdown func()
	shutdown = func() {
		shutdown = func() {} // Make this function an empty one after first run to prevent calling the following goroutine multiple times
		// Do this asyncrounously because getting the lock might block a .Store() that's
		// waiting on us to read from 'upstream'!  We don't need to worry about separately
		// waiting for this goroutine because we implicitly do that when we drain
		// 'upstream'.
		go func() {
			tm.lock.Lock()
			defer tm.lock.Unlock()
			close(tm.subscribers[upstream])
			delete(tm.subscribers, upstream)
		}()
	}

	// Cur is a snapshot of the current state all the map according to all MAPTYPEUpdates we've
	// received from 'upstream', with any entries removed that do not satisfy the predicate
	// 'includep'.
	cur := make(map[string]VALTYPE)
	for k, v := range tm.LoadAll() {
		if includep(k, v) {
			cur[k] = v
		}
	}

	snapshot := MAPTYPESnapshot{
		// snapshot.State is a copy of 'cur' that we send to the 'downstream' channel.  We
		// don't send 'cur' directly because we're nescessarily in a separate goroutine from
		// the reader of 'downstream', and map gets/sets aren't thread-safe, so we'd risk
		// memory corruption with our updating of 'cur' and the reader's accessing of 'cur'.
		// snapshot.State gets set to 'nil' when we need to do a read before we can write to
		// 'downstream' again.
		State: make(map[string]VALTYPE, len(cur)),

		Updates: nil,
	}
	for k, v := range cur {
		snapshot.State[k] = v
	}

	// applyUpdate applies an update to 'cur', and updates 'snapshot.State' as nescessary.
	applyUpdate := func(update MAPTYPEUpdate) {
		if update.Delete || !includep(update.Key, update.Value) {
			if old, haveOld := cur[update.Key]; haveOld {
				update.Delete = true
				update.Value = old
				snapshot.Updates = append(snapshot.Updates, update)
				delete(cur, update.Key)
				if snapshot.State != nil {
					delete(snapshot.State, update.Key)
				} else {
					snapshot.State = make(map[string]VALTYPE, len(cur))
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
					snapshot.State = make(map[string]VALTYPE, len(cur))
					for k, v := range cur {
						snapshot.State[k] = v
					}
				}
			}
		}
	}

	for {
		if snapshot.State == nil {
			select {
			case <-ctx.Done():
				shutdown()
				return
			case <-tm.close:
				shutdown()
				return
			case update, readOK := <-upstream:
				if !readOK {
					return
				}
				applyUpdate(update)
			}
		} else {
			// Same as above, but with an additional "downstream <- snapshot" case.
			select {
			case <-ctx.Done():
				shutdown()
				return
			case <-tm.close:
				shutdown()
				return
			case update, readOK := <-upstream:
				if !readOK {
					return
				}
				applyUpdate(update)
			case downstream <- snapshot:
				snapshot = MAPTYPESnapshot{}
			}
		}
	}
}
