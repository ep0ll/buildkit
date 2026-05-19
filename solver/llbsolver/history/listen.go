package history

import (
	"context"
	"os"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
)

// Listen streams build-history events and optionally live status for one ref.
func (q *Queue) Listen(ctx context.Context, req *controlapi.BuildHistoryRequest, emit func(*controlapi.BuildHistoryEvent) error) error {
	q.init()

	sub, unlock, err := q.prepareListen(req)
	if err != nil {
		return err
	}
	defer unlock()
	defer sub.close()

	if err := q.emitInitialEvents(ctx, req, emit); err != nil {
		return err
	}
	if req.EarlyExit {
		return nil
	}
	return q.streamEvents(ctx, req, sub, emit)
}

type listenSubscription struct {
	events *channel[*controlapi.BuildHistoryEvent]
}

func (s *listenSubscription) close() {
	s.events.close()
}

func (q *Queue) prepareListen(req *controlapi.BuildHistoryRequest) (*listenSubscription, func(), error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if req.Ref != "" {
		if _, deleted := q.pendingDel[req.Ref]; deleted {
			return nil, nil, errors.Wrapf(os.ErrNotExist, "ref %s is deleted", req.Ref)
		}
		q.lockRecord(req.Ref)
	}

	sub := &listenSubscription{events: q.events.Subscribe()}
	unlock := func() {}
	if req.Ref != "" {
		ref := req.Ref
		unlock = func() {
			q.mu.Lock()
			q.unlockRecord(ref)
			q.mu.Unlock()
		}
	}
	return sub, unlock, nil
}

func (q *Queue) emitInitialEvents(ctx context.Context, req *controlapi.BuildHistoryRequest, emit func(*controlapi.BuildHistoryEvent) error) error {
	actives, err := q.snapshotActiveEvents(req)
	if err != nil {
		return err
	}
	for _, e := range actives {
		if err := emit(e); err != nil {
			return err
		}
	}
	if req.ActiveOnly {
		return nil
	}
	return q.emitStoredEvents(req, emit)
}

func (q *Queue) snapshotActiveEvents(req *controlapi.BuildHistoryRequest) ([]*controlapi.BuildHistoryEvent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make([]*controlapi.BuildHistoryEvent, 0, len(q.active))
	for _, rec := range q.active {
		if !matchesRef(req.Ref, rec.Ref) {
			continue
		}
		if _, deleted := q.pendingDel[rec.Ref]; deleted {
			continue
		}
		out = append(out, &controlapi.BuildHistoryEvent{
			Type:   controlapi.BuildHistoryEventType_STARTED,
			Record: rec,
		})
	}
	return out, nil
}

func (q *Queue) emitStoredEvents(req *controlapi.BuildHistoryRequest, emit func(*controlapi.BuildHistoryEvent) error) error {
	events, err := q.loadStoredEvents(req.Ref)
	if err != nil {
		return err
	}
	events = q.filterPendingDeletes(events)
	events, err = filterHistoryEvents(events, req.Filter, req.Limit)
	if err != nil {
		return err
	}
	for _, e := range events {
		if e == nil || e.Record == nil {
			continue
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) loadStoredEvents(ref string) ([]*controlapi.BuildHistoryEvent, error) {
	var events []*controlapi.BuildHistoryEvent
	err := q.opt.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(recordsBucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(key, dt []byte) error {
			if ref != "" && ref != string(key) {
				return nil
			}
			var rec controlapi.BuildHistoryRecord
			if err := rec.UnmarshalVT(dt); err != nil {
				return errors.Wrapf(err, "failed to unmarshal build record %s", key)
			}
			events = append(events, &controlapi.BuildHistoryEvent{
				Record: &rec,
				Type:   controlapi.BuildHistoryEventType_COMPLETE,
			})
			return nil
		})
	})
	return events, err
}

func (q *Queue) filterPendingDeletes(events []*controlapi.BuildHistoryEvent) []*controlapi.BuildHistoryEvent {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, e := range events {
		if _, ok := q.pendingDel[e.Record.Ref]; ok {
			events[i] = nil
		}
	}
	return events
}

func (q *Queue) streamEvents(ctx context.Context, req *controlapi.BuildHistoryRequest, sub *listenSubscription, emit func(*controlapi.BuildHistoryEvent) error) error {
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case e := <-sub.events.ch:
			if !matchesRef(req.Ref, e.Record.Ref) {
				continue
			}
			if err := emit(e); err != nil {
				return err
			}
		case <-sub.events.done:
			return nil
		}
	}
}

func matchesRef(filter, ref string) bool {
	return filter == "" || filter == ref
}
