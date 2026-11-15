package core

import "sync"

// Broker fans out "something changed" signals. Subscribers re-read state;
// signals coalesce so slow subscribers never block the core.
type Broker struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// NewBroker returns an empty broker.
func NewBroker() *Broker { return &Broker{subs: map[chan struct{}]struct{}{}} }

// Subscribe returns a signal channel and an unsubscribe func.
func (b *Broker) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Notify signals every subscriber without blocking.
func (b *Broker) Notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
