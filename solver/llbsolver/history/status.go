package history

import (
	"bufio"
	"context"
	"os"

	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
)

// Status streams solve status for a build: live while running, replay when stored.
func (q *Queue) Status(ctx context.Context, ref string, out chan<- *client.SolveStatus) error {
	q.init()

	if q.activeStatusPipeline(ref) != nil {
		return q.streamLiveStatus(ctx, ref, out)
	}

	rec, err := q.loadRecord(ref)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if rec.Logs == nil {
		return nil
	}
	return replayStatusBlob(ctx, q.hContentStore, rec.Logs, out)
}

// ImportStatus drains statusCh, publishes live updates, and returns a blob descriptor.
func (q *Queue) ImportStatus(ctx context.Context, statusCh chan *client.SolveStatus) (*StatusImportResult, func(), error) {
	defer drainStatusChannel(statusCh)

	w, err := q.OpenBlobWriter(ctx, "application/vnd.buildkit.status.v0")
	if err != nil {
		return nil, nil, err
	}

	imported, err := q.importStatusFromChannel(ctx, w, bufio.NewWriter(w), statusCh)
	if err != nil {
		w.Discard()
		return nil, nil, err
	}
	return imported.StatusImportResult, imported.release, nil
}

// ImportStatusFromPipeline persists frames from the status pipeline for ref.
func (q *Queue) ImportStatusFromPipeline(ctx context.Context, ref string) (*StatusImportResult, func(), error) {
	p := q.activeStatusPipeline(ref)
	if p == nil {
		return nil, nil, errors.Errorf("no status pipeline for ref %s", ref)
	}
	<-p.Wait()

	q.mu.Lock()
	delete(q.liveStatus, ref)
	q.mu.Unlock()

	w, err := q.OpenBlobWriter(ctx, "application/vnd.buildkit.status.v0")
	if err != nil {
		return nil, nil, err
	}

	metrics := newStatusMetrics()
	bufW := bufio.NewWriter(w)
	for _, frame := range p.bufferedFrames() {
		if err := importStatusFrame(bufW, frame, metrics); err != nil {
			w.Discard()
			return nil, nil, err
		}
	}
	imported, err := q.finishStatusImport(ctx, w, bufW, metrics)
	if err != nil {
		return nil, nil, err
	}
	return imported.StatusImportResult, imported.release, nil
}

func drainStatusChannel(ch chan *client.SolveStatus) {
	if ch == nil {
		return
	}
	for range ch {
	}
}

func (q *Queue) importStatusFromChannel(ctx context.Context, w *blobWriter, bufW *bufio.Writer, statusCh chan *client.SolveStatus) (*statusImportResult, error) {
	metrics := newStatusMetrics()
	buf := make([]byte, 32*1024)

	for st := range statusCh {
		metrics.observe(st)
		for _, msg := range st.Marshal() {
			var err error
			buf, err = writeStatusFrame(bufW, buf, msg)
			if err != nil {
				return nil, err
			}
		}
	}
	return q.finishStatusImport(ctx, w, bufW, metrics)
}

type statusImportResult struct {
	*StatusImportResult
	release func()
}

func (q *Queue) finishStatusImport(ctx context.Context, w *blobWriter, bufW *bufio.Writer, metrics *statusMetrics) (*statusImportResult, error) {
	if err := bufW.Flush(); err != nil {
		return nil, err
	}
	desc, release, err := w.Commit(ctx)
	if err != nil {
		return nil, err
	}
	return &statusImportResult{
		StatusImportResult: metrics.result(*desc),
		release:            release,
	}, nil
}
