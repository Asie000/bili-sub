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
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
}

// subscriber is one SSE stream. Closing its channel is what tells the handler to
// return, and it has to happen exactly once -- both the client leaving and a
// shutdown close it, and a double close would panic.
type subscriber struct {
	ch   chan Event
	once sync.Once
}

func (s *subscriber) stop() {
	s.once.Do(func() { close(s.ch) })
}

func NewHub() *Hub {
	return &Hub{subs: make(map[*subscriber]struct{})}
}

// Subscribe registers a listener and returns its channel plus the cancel func
// that unregisters it and closes the channel. cancel is idempotent, and safe to
// call after Close. A hub that is already closed hands back a closed channel, so
// a handler that arrives during shutdown returns instead of waiting forever.
func (h *Hub) Subscribe() (chan Event, func()) {
	sub := &subscriber{ch: make(chan Event, subscriberBuffer)}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		sub.stop()
		return sub.ch, func() {}
	}
	if h.subs == nil {
		h.subs = make(map[*subscriber]struct{})
	}
	h.subs[sub] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		delete(h.subs, sub)
		h.mu.Unlock()
		// Closing outside the lock is safe because Broadcast only ever sends to
		// channels still in the map, and this removes it first.
		sub.stop()
	}
	return sub.ch, cancel
}

// Broadcast delivers ev to every subscriber, dropping the event for any
// subscriber whose buffer is full so one slow client cannot stall the callers.
// A zero-value Hub has no subscribers, so the nil map is simply empty.
func (h *Hub) Broadcast(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

// Close ends every subscription and refuses new ones. It exists for shutdown:
// http.Server.Shutdown closes idle connections and then waits for the active
// ones, and it never cancels request contexts, so an open /api/events stream
// keeps the server "active" until the grace period runs out. Closing the streams
// first is what makes Ctrl+C with a browser page open shut down immediately.
// Close is idempotent.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for sub := range h.subs {
		delete(h.subs, sub)
		sub.stop()
	}
}
