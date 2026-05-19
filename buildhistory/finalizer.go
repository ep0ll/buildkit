package buildhistory

import (
	"context"
	"sync"
)

// AcquireFinalizer registers a build awaiting trace export before finalize.
func (q *Queue) AcquireFinalizer(ref string) (<-chan struct{}, func()) {
	q.mu.Lock()
	defer q.mu.Unlock()

	trigger := make(chan struct{})
	f := &recordFinalizer{
		trigger: sync.OnceFunc(func() { close(trigger) }),
		done:    make(chan struct{}),
	}
	q.finalizers[ref] = f
	go q.watchFinalizer(ref, f)
	return trigger, sync.OnceFunc(func() { close(f.done) })
}

func (q *Queue) watchFinalizer(ref string, f *recordFinalizer) {
	<-f.done
	q.mu.Lock()
	delete(q.finalizers, ref)
	q.maybeCloseEventsOnShutdown()
	q.mu.Unlock()
}

func (q *Queue) maybeCloseEventsOnShutdown() {
	if len(q.finalizers) != 0 {
		return
	}
	select {
	case <-q.opt.GracefulStop:
		go q.events.Close()
	default:
	}
}

// Finalize waits until trace export for a build has been triggered and completed.
func (q *Queue) Finalize(ctx context.Context, ref string) error {
	q.mu.Lock()
	f, ok := q.finalizers[ref]
	q.mu.Unlock()
	if !ok {
		return nil
	}
	f.trigger()
	<-f.done
	return nil
}
