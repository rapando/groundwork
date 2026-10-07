// Package events is the in-process pub/sub that feeds the SSE stream.
package events

import (
	"encoding/json"
	"sync"
)

// Event is one message on the bus. Type is the SSE event name
// (file.changed, run.updated, log.line, ...).
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Bus fans events out to subscribers. Publishing never blocks: a subscriber
// whose buffer is full simply misses the event (clients refetch on reconnect).
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

// Subscribe returns a receive channel and a cancel func that must be called.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
}

// Publish marshals data (may be nil) and delivers to all subscribers.
func (b *Bus) Publish(typ string, data any) {
	ev := Event{Type: typ}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return
		}
		ev.Data = raw
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
