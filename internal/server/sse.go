// Package server exposes the store and the CLI's pipeline seams over a small
// JSON HTTP API plus an SSE stream, and serves the embedded browser UI.
package server

import "sync"

// Event is one SSE message. It is marshalled as the single `data:` payload of
// the event, e.g. `data: {"type":"probe","data":{...}}`.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// subscriberBuffer is how many undelivered events a subscriber may accumulate.
// A browser that cannot keep up is dropped from rather than allowed to block
// the broadcaster.
const subscriberBuffer = 16

// Hub fans an Event out to every current subscriber. It is safe for concurrent
// use by HTTP handlers and pipeline callbacks. The zero value is a usable hub
// with no subscribers, so Options{Hub: &Hub{}} cannot panic.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: make(map[chan Event]struct{})}
}

// Subscribe registers a listener and returns its channel plus the cancel func
// that unregisters it and closes the channel. cancel is idempotent.
func (h *Hub) Subscribe() (chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = make(map[chan Event]struct{})
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			close(ch)
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// Broadcast delivers ev to every subscriber, dropping the event for any
// subscriber whose buffer is full so one slow client cannot stall the callers.
// A zero-value Hub has no subscribers, so the nil map is simply empty.
func (h *Hub) Broadcast(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
