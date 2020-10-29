package manager

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Entity interface {
}

// PresenceEntry is one entry in the Presence set.
type PresenceEntry struct {
	presence time.Time
	ctx      context.Context
	cancel   context.CancelFunc
	item     Entity
}

// Context returns the context for this entry.
func (entry *PresenceEntry) Context() context.Context {
	return entry.ctx
}

// Item returns the tracked entity for this entry.
func (entry *PresenceEntry) Item() Entity {
	return entry.item
}

type PresenceRemoveFunc func(context.Context, string, Entity)

// Presence keeps a mapping of string IDs to arbitrary entities, tracking when
// they were last marked as being present.
type Presence struct {
	entries  map[string]*PresenceEntry
	ctx      context.Context
	onRemove PresenceRemoveFunc
}

// NewPresence takes the base context for the tracked entities and a function
// that gets called when an entity is removed.
func NewPresence(ctx context.Context, onRemove PresenceRemoveFunc) *Presence {
	if onRemove == nil {
		onRemove = func(context.Context, string, Entity) {}
	}
	return &Presence{
		entries:  make(map[string]*PresenceEntry),
		ctx:      ctx,
		onRemove: onRemove,
	}
}

// Add an ID and entity to the set.
func (p Presence) Add(id string, item Entity, now time.Time) {
	if entry, ok := p.entries[id]; ok {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", id, entry.item, item))
	}

	ctx, cancel := context.WithCancel(p.ctx)
	p.entries[id] = &PresenceEntry{
		presence: now,
		ctx:      ctx,
		cancel:   cancel,
		item:     item,
	}
}

// IsPresent returns whether an ID exists in the set of present IDs.
func (p Presence) IsPresent(id string) bool {
	_, ok := p.entries[id]
	return ok
}

// Mark an ID as being present at the indicated time.
func (p Presence) Mark(id string, now time.Time) error {
	if entry, ok := p.entries[id]; ok {
		entry.presence = now
		return nil
	}

	return errors.New("session not found")
}

// Get the entry for the associated ID.
func (p Presence) Get(id string) *PresenceEntry {
	return p.entries[id]
}

// Remove an ID from the set of present IDs and return its associated entry.
func (p Presence) Remove(id string) *PresenceEntry {
	if entry, ok := p.entries[id]; ok {
		delete(p.entries, id)
		entry.cancel()
		p.onRemove(entry.ctx, id, entry.item)
		return entry
	}

	return nil
}

// Expire (remove) entries whose last update is older than the indicated time.
func (p Presence) Expire(moment time.Time) {
	for id, entry := range p.entries {
		if entry.presence.Before(moment) {
			_ = p.Remove(id)
		}
	}
}
