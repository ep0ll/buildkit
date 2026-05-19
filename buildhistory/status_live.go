package buildhistory

import (
	"context"
	"sync"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/buildhistory/internal/pubsub"
	"github.com/moby/buildkit/client"
)

// statusPipeline fans solve status to live subscribers and buffers frames for import.
type statusPipeline struct {
	mu sync.Mutex

	in     chan *client.SolveStatus
	events *pubsub.Pubsub[*controlapi.StatusResponse]
	done   chan struct{}

	frames [][]byte
}

func newStatusPipeline() *statusPipeline {
	p := &statusPipeline{
		in:     make(chan *client.SolveStatus, 32),
		events: pubsub.New[*controlapi.StatusResponse](),
		done:   make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *statusPipeline) run() {
	defer close(p.done)
	defer p.events.Close()

	for st := range p.in {
		p.broadcast(st)
		p.bufferFrames(st)
	}
}

func (p *statusPipeline) broadcast(st *client.SolveStatus) {
	for _, msg := range st.Marshal() {
		p.events.Send(msg)
	}
}

func (p *statusPipeline) bufferFrames(st *client.SolveStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()

	buf := make([]byte, 32*1024)
	for _, msg := range st.Marshal() {
		sz := msg.SizeVT()
		if cap(buf) < sz {
			buf = make([]byte, sz)
		}
		n, err := msg.MarshalToVT(buf[:sz])
		if err != nil {
			continue
		}
		frame := append([]byte(nil), buf[:n]...)
		p.frames = append(p.frames, frame)
	}
}

func (p *statusPipeline) Input() chan *client.SolveStatus {
	return p.in
}

func (p *statusPipeline) Wait() <-chan struct{} {
	return p.done
}

func (p *statusPipeline) Close() {
	close(p.in)
}

func (p *statusPipeline) Subscribe() *pubsub.Channel[*controlapi.StatusResponse] {
	return p.events.Subscribe()
}

func (p *statusPipeline) bufferedFrames() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.frames))
	copy(out, p.frames)
	return out
}

func (q *Queue) beginStatusPipeline(ref string) *statusPipeline {
	q.mu.Lock()
	defer q.mu.Unlock()

	if p, ok := q.liveStatus[ref]; ok {
		return p
	}
	p := newStatusPipeline()
	q.liveStatus[ref] = p
	return p
}

func (q *Queue) activeStatusPipeline(ref string) *statusPipeline {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.liveStatus[ref]
}

// OpenStatusInput returns the channel that receives live solve status for a build.
func (q *Queue) OpenStatusInput(ref string) chan *client.SolveStatus {
	return q.beginStatusPipeline(ref).Input()
}

// CloseStatusInput stops ingestion; call ImportStatusFromPipeline after j.Status returns.
func (q *Queue) CloseStatusInput(ref string) {
	if p := q.activeStatusPipeline(ref); p != nil {
		p.Close()
	}
}

func (q *Queue) streamLiveStatus(ctx context.Context, ref string, out chan<- *client.SolveStatus) error {
	p := q.activeStatusPipeline(ref)
	if p == nil {
		return nil
	}
	sub := p.Subscribe()
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case msg, ok := <-sub.Ch:
			if !ok {
				return nil
			}
			out <- client.NewSolveStatus(msg)
		case <-sub.Done:
			return nil
		}
	}
}
