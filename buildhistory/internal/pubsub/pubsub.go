// Package pubsub provides a small generic fan-out broadcaster for build history.
package pubsub

import "sync"

// Pubsub broadcasts values to subscribed channels.
type Pubsub[T any] struct {
	mu sync.Mutex
	m  map[*Channel[T]]struct{}
}

// New creates a Pubsub.
func New[T any]() *Pubsub[T] {
	return &Pubsub[T]{m: map[*Channel[T]]struct{}{}}
}

// Subscribe returns a channel that receives broadcast values.
func (p *Pubsub[T]) Subscribe() *Channel[T] {
	p.mu.Lock()
	c := &Channel[T]{
		ps:   p,
		Ch:   make(chan T, 32),
		Done: make(chan struct{}),
	}
	p.m[c] = struct{}{}
	p.mu.Unlock()
	return c
}

// Send delivers v to all subscribers.
func (p *Pubsub[T]) Send(v T) {
	p.mu.Lock()
	for c := range p.m {
		go c.send(v)
	}
	p.mu.Unlock()
}

// Close closes all subscriber channels.
func (p *Pubsub[T]) Close() {
	p.mu.Lock()
	channels := make([]*Channel[T], 0, len(p.m))
	for c := range p.m {
		channels = append(channels, c)
	}
	p.mu.Unlock()
	for _, c := range channels {
		c.Close()
	}
}

// Channel receives values from a Pubsub subscription.
type Channel[T any] struct {
	ps        *Pubsub[T]
	Ch        chan T
	Done      chan struct{}
	closeOnce sync.Once
}

func (p *Channel[T]) send(v T) {
	select {
	case p.Ch <- v:
	case <-p.Done:
	}
}

// Close unsubscribes and signals Done.
func (p *Channel[T]) Close() {
	p.closeOnce.Do(func() {
		p.ps.mu.Lock()
		delete(p.ps.m, p)
		p.ps.mu.Unlock()
		close(p.Done)
	})
}
