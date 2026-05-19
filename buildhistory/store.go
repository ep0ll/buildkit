package buildhistory

import (
	"context"
	"os"

	"github.com/containerd/containerd/v2/core/leases"
	cerrdefs "github.com/containerd/errdefs"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
)

func (q *Queue) Update(ctx context.Context, e *controlapi.BuildHistoryEvent) error {
	q.init()
	q.mu.Lock()
	defer q.mu.Unlock()

	e = e.CloneVT()
	switch e.Type {
	case controlapi.BuildHistoryEventType_STARTED:
		return q.handleBuildStarted(e)
	case controlapi.BuildHistoryEventType_COMPLETE:
		return q.handleBuildCompleted(ctx, e)
	default:
		return nil
	}
}

func (q *Queue) handleBuildStarted(e *controlapi.BuildHistoryEvent) error {
	q.active[e.Record.Ref] = e.Record
	q.events.Send(e)
	return q.runOnEvent(context.Background(), e)
}

func (q *Queue) handleBuildCompleted(ctx context.Context, e *controlapi.BuildHistoryEvent) error {
	delete(q.active, e.Record.Ref)
	if err := q.persistRecord(ctx, e.Record); err != nil {
		return err
	}
	q.events.Send(e)
	return q.runOnEvent(ctx, e)
}

func (q *Queue) runOnEvent(ctx context.Context, e *controlapi.BuildHistoryEvent) error {
	if fn := q.opt.Hooks.OnEvent; fn != nil {
		return fn(ctx, e)
	}
	return nil
}

func (q *Queue) UpdateRef(ctx context.Context, ref string, mutate func(*controlapi.BuildHistoryRecord) error) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	rec, err := q.loadRecord(ref)
	if err != nil {
		return err
	}
	if err := mutate(rec); err != nil {
		return err
	}
	if rec.Ref != ref {
		return errors.Errorf("invalid ref change")
	}
	rec.Generation++
	return q.saveRecordUpdate(ctx, rec)
}

func (q *Queue) saveRecordUpdate(ctx context.Context, rec *controlapi.BuildHistoryRecord) error {
	if err := q.persistRecord(ctx, rec); err != nil {
		return err
	}
	q.events.Send(&controlapi.BuildHistoryEvent{
		Type:   controlapi.BuildHistoryEventType_COMPLETE,
		Record: rec,
	})
	return nil
}

func (q *Queue) loadRecord(ref string) (*controlapi.BuildHistoryRecord, error) {
	var rec controlapi.BuildHistoryRecord
	err := q.opt.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(recordsBucket))
		if b == nil {
			return errors.Wrapf(os.ErrNotExist, "failed to retrieve bucket %s", recordsBucket)
		}
		dt := b.Get([]byte(ref))
		if dt == nil {
			return errors.Wrapf(os.ErrNotExist, "failed to retrieve ref %s", ref)
		}
		return rec.UnmarshalVT(dt)
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (q *Queue) persistRecord(ctx context.Context, rec *controlapi.BuildHistoryRecord) error {
	return q.opt.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(recordsBucket))
		if b == nil {
			return nil
		}
		dt, err := rec.MarshalVT()
		if err != nil {
			return err
		}
		l, created, err := q.ensureRecordLease(ctx, rec.Ref)
		if err != nil {
			return err
		}
		defer q.releaseLeaseOnError(ctx, l, created, &err)
		if err := q.attachRecordResources(ctx, l, rec); err != nil {
			return err
		}
		return b.Put([]byte(rec.Ref), dt)
	})
}

func (q *Queue) ensureRecordLease(ctx context.Context, ref string) (leases.Lease, bool, error) {
	l, err := q.hLeaseManager.Create(ctx, leases.WithID(q.leaseID(ref)))
	if err == nil {
		return l, true, nil
	}
	if !errors.Is(err, cerrdefs.ErrAlreadyExists) {
		return leases.Lease{}, false, err
	}
	return leases.Lease{ID: q.leaseID(ref)}, false, nil
}

func (q *Queue) releaseLeaseOnError(ctx context.Context, l leases.Lease, created bool, err *error) {
	if *err != nil && created {
		q.hLeaseManager.Delete(context.WithoutCancel(ctx), l)
	}
}

func (q *Queue) attachRecordResources(ctx context.Context, l leases.Lease, rec *controlapi.BuildHistoryRecord) error {
	if err := q.addResource(ctx, l, rec.Logs, false); err != nil {
		return err
	}
	if err := q.addResource(ctx, l, rec.Trace, false); err != nil {
		return err
	}
	if err := q.addResource(ctx, l, rec.ExternalError, false); err != nil {
		return err
	}
	return q.attachResultResources(ctx, l, rec)
}

func (q *Queue) attachResultResources(ctx context.Context, l leases.Lease, rec *controlapi.BuildHistoryRecord) error {
	if rec.Result != nil {
		if err := q.attachBuildResult(ctx, l, rec.Result); err != nil {
			return err
		}
	}
	for _, r := range rec.Results {
		if err := q.attachBuildResult(ctx, l, r); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) attachBuildResult(ctx context.Context, l leases.Lease, info *controlapi.BuildResultInfo) error {
	if err := q.addResource(ctx, l, info.ResultDeprecated, true); err != nil {
		return err
	}
	for _, res := range info.Results {
		if err := q.addResource(ctx, l, res, true); err != nil {
			return err
		}
	}
	for _, att := range info.Attestations {
		if err := q.addResource(ctx, l, att, false); err != nil {
			return err
		}
	}
	return nil
}
